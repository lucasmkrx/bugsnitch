// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestDevicePairingHasOneRecipientUnderContention(t *testing.T) {
	s := fixture(t)
	c := login(t, s)
	project := c.post(t, "projects", object{"name": "App", "repository": "example/app"}, 201)["id"].(string)
	code := c.post(t, "pairings", object{"name": "Mac", "projectIds": []string{project}}, 201)["code"].(string)
	keys := []string{random(32), random(32)}
	type response struct {
		key    int
		status int
		body   object
	}
	var responses [2]response
	var wg sync.WaitGroup
	start := make(chan struct{})
	pair := func(key string) (int, object) {
		return c.req(t, "POST", "/api/v1/device/pair", protocol.Pair{
			Protocol: 1, Code: code, Credential: key, Platform: "darwin/arm64",
		}, "")
	}
	for i, key := range keys {
		wg.Go(func() {
			<-start
			status, body := pair(key)
			responses[i] = response{i, status, body}
		})
	}
	close(start)
	wg.Wait()
	first, second := responses[0], responses[1]
	if first.status != 200 {
		first, second = second, first
	}
	if first.status != 200 || second.status != 403 {
		t.Fatalf("concurrent enrollment returned %d and %d", first.status, second.status)
	}
	if status, body := pair(keys[first.key]); status != 200 || body["deviceId"] != first.body["deviceId"] {
		t.Fatal("lost response could not be recovered with the original credential", status)
	}
	var devices, paired, bindings int
	err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM devices),
		(SELECT count(*) FROM events WHERE kind='device.paired' AND actor_id=$1),
		(SELECT count(*) FROM device_projects WHERE device_id=$1 AND project_id=$2)`,
		first.body["deviceId"], project).Scan(&devices, &paired, &bindings)
	if err != nil || devices != 1 || paired != 1 || bindings != 1 {
		t.Fatalf("devices=%d pairing events=%d project bindings=%d err=%v", devices, paired, bindings, err)
	}
}

func TestWorkflowReasonUpgradeAndLegacyReplay(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	token, _, report := runningDevice(t, owner)
	report.State = "waiting"
	report.Phase = "implement"
	report.Version++
	sync := func(want int) {
		t.Helper()
		status, result := owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{
			Protocol: 1, Attempts: []protocol.Attempt{report},
		}, token)
		if status != want || (want == 200 && result["workflowReasons"] != true) {
			t.Fatalf("sync: %d %v", status, result)
		}
	}
	sync(200)
	report.ReasonCode = protocol.PlanReviewRequired
	sync(200)
	report.ReasonCode = ""
	sync(200)
	report.ReasonCode = protocol.OwnerInputRequired
	sync(409)
	var reason string
	if err := s.DB.QueryRow("SELECT reason_code FROM attempt_reports WHERE id=$1", report.ID).Scan(&reason); err != nil || reason != protocol.PlanReviewRequired {
		t.Fatalf("stored reason = %q, %v", reason, err)
	}
}

// Force a reconnect and emergency stop to reach the same pending pause.
// The emergency owns that row first. The reconnect cannot hold the event
// cursor lock while waiting to acknowledge the superseded pause.
func TestDeviceReconnectWhileEmergencySupersedesPause(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	token, device, report := runningDevice(t, owner)
	pauseID := owner.post(t, "attempts/"+report.ID+"/control", controlInput{Operation: "pause", Version: 1, RequestID: "pause_reconnect"}, 202)["id"].(string)
	if _, err := s.DB.Exec("UPDATE devices SET last_contact=now()-interval '1 hour' WHERE id=$1", device); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gate, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	var gatePID int
	if err := gate.QueryRowContext(ctx, "SELECT pg_backend_pid() FROM controls WHERE id=$1 FOR UPDATE", pauseID).Scan(&gatePID); err != nil {
		t.Fatal(err)
	}
	stopResult := make(chan int, 1)
	go func() {
		defer close(stopResult)
		status, _ := owner.req(t, "POST", "/api/v1/workspace/dispatch", object{"version": 1, "requestId": "stop_on_reconnect", "stopped": true}, "")
		stopResult <- status
	}()
	stopPID := waitForBlockedWriter(t, ctx, s.DB, gatePID)
	syncResult := make(chan int, 1)
	go func() {
		defer close(syncResult)
		status, _ := owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Acks: []protocol.Ack{{ID: pauseID, Status: "received"}}}, token)
		syncResult <- status
	}()
	waitForBlockedWriter(t, ctx, s.DB, stopPID)
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	if stop, reconnect := <-stopResult, <-syncResult; stop != 200 || reconnect != 200 {
		t.Fatalf("emergency stop returned %d, reconnect returned %d", stop, reconnect)
	}
	var state string
	var stops, reconnects int
	err = s.DB.QueryRow(`SELECT (SELECT status FROM controls WHERE id=$1),
		(SELECT count(*) FROM controls WHERE attempt_id=$2 AND operation='stop' AND status='requested'),
		(SELECT count(*) FROM events WHERE kind='device.reconnected' AND subject_id=$3)`,
		pauseID, report.ID, device).Scan(&state, &stops, &reconnects)
	if err != nil || state != "superseded" || stops != 1 || reconnects != 1 {
		t.Fatalf("pause=%s requested stops=%d reconnects=%d err=%v", state, stops, reconnects, err)
	}
}

func TestDeviceSyncRollsBackReportsAndReconnectWhenAckIsDenied(t *testing.T) {
	s := fixture(t)
	c := login(t, s)
	key, device, report := runningDevice(t, c)
	sync := func(req protocol.Sync, want int) {
		t.Helper()
		got, body := c.req(t, "POST", "/api/v1/device/sync", req, key)
		if got != want {
			t.Fatalf("sync returned %d, want %d: %v", got, want, body)
		}
	}
	control := c.post(t, "attempts/"+report.ID+"/control", controlInput{Operation: "stop", Version: 1, RequestID: "atomic_stop_request"}, 202)["id"].(string)
	_, err := s.DB.Exec("UPDATE devices SET last_contact=now()-interval '1 hour' WHERE id=$1", device)
	if err != nil {
		t.Fatal(err)
	}
	report.Version++
	batch := protocol.Sync{Protocol: 1, Attempts: []protocol.Attempt{report}, Acks: []protocol.Ack{
		{ID: control, Status: "received"}, {ID: "unknown_control", Status: "completed"},
	}}
	sync(batch, 403)
	var rolledBack bool
	err = s.DB.QueryRow(`SELECT
		(SELECT last_contact<now()-interval '2 minutes' FROM devices WHERE id=$1)
		AND (SELECT version=1 FROM attempt_reports WHERE id=$2)
		AND (SELECT status='requested' FROM controls WHERE id=$3)
		AND NOT EXISTS(SELECT FROM events WHERE kind IN ('control.received','device.reconnected'))`,
		device, report.ID, control).Scan(&rolledBack)
	if err != nil || !rolledBack {
		t.Fatal("failed batch left a heartbeat, report, acknowledgment or event", err)
	}
	batch.Acks = batch.Acks[:1]
	sync(batch, 200)
	sync(batch, 200)
	var reports, acks, reconnects int
	err = s.DB.QueryRow(`SELECT count(*) FILTER (WHERE kind='attempt.reported' AND subject_id=$1 AND subject_version=2),
		count(*) FILTER (WHERE kind='control.received' AND subject_id=$1),
		count(*) FILTER (WHERE kind='device.reconnected' AND subject_id=$2) FROM events`,
		report.ID, device).Scan(&reports, &acks, &reconnects)
	if err != nil || reports != 1 || acks != 1 || reconnects != 1 {
		t.Fatalf("retry events: reports=%d acks=%d reconnects=%d err=%v", reports, acks, reconnects, err)
	}
}

// A stopped local worker can lose the start acknowledgement on either side
// of the hosted commit. Both histories must converge, even while paused.
func TestAbandonUncertainTurn(t *testing.T) {
	for _, start := range []string{"committed", "in_transit"} {
		t.Run(start, func(t *testing.T) {
			s := fixture(t)
			owner := login(t, s)
			token, _, report := runningDevice(t, owner)
			a := protocol.Admission{Protocol: 1, ProjectID: report.ProjectID, AttemptID: report.ID, IssueNumber: report.IssueNumber, BaseSHA: report.BaseSHA}
			send := func(op string, seq int64, want int) {
				t.Helper()
				a.Operation, a.Sequence = op, seq
				got, body := owner.req(t, "POST", "/api/v1/device/admission", a, token)
				if got != want {
					t.Fatalf("%s at %d returned %d %v, want %d", op, seq, got, body, want)
				}
			}
			send("turn_abandon", 2, 409)
			if start == "committed" {
				send("turn_start", 2, 200)
			}
			if _, err := s.DB.Exec("UPDATE projects SET paused=true WHERE id=$1", report.ProjectID); err != nil {
				t.Fatal(err)
			}
			send("turn_abandon", 4, 409)
			send("turn_abandon", 3, 200)
			send("turn_abandon", 3, 200)
			if _, err := s.DB.Exec("UPDATE projects SET paused=false WHERE id=$1", report.ProjectID); err != nil {
				t.Fatal(err)
			}
			send("turn_start", 2, 409)
			var active, owned, inTurn bool
			if err := s.DB.QueryRow("SELECT active,owns_issue,in_turn FROM admission WHERE workspace_id='personal' AND attempt_id=$1", a.AttemptID).Scan(&active, &owned, &inTurn); err != nil || !active || !owned || inTurn {
				t.Fatalf("after reconciliation: active=%v owned=%v inTurn=%v err=%v", active, owned, inTurn, err)
			}
			a.State = "cancelled"
			send("finish", 4, 200)
			send("turn_abandon", 6, 409)
		})
	}
}

func runningDevice(t *testing.T, owner system) (token, device string, report protocol.Attempt) {
	t.Helper()
	project := owner.post(t, "projects", object{"name": "App", "repository": "example/app"}, 201)["id"].(string)
	code := owner.post(t, "pairings", object{"name": "Mac", "projectIds": []string{project}}, 201)["code"].(string)
	token = random(32)
	device = owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)["deviceId"].(string)
	report = protocol.Attempt{ProjectID: project, ID: "running_attempt", IssueNumber: 1, BaseSHA: strings.Repeat("a", 40), Phase: "implement", State: "running", Version: 1}
	req := protocol.Admission{Protocol: 1, ProjectID: project, AttemptID: report.ID, IssueNumber: 1, BaseSHA: report.BaseSHA, Operation: "admit", Sequence: 1}
	if status, _ := owner.req(t, "POST", "/api/v1/device/admission", req, token); status != 200 {
		t.Fatal("admission failed", status)
	}
	if status, _ := owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Attempts: []protocol.Attempt{report}}, token); status != 200 {
		t.Fatal("first sync failed", status)
	}
	return token, device, report
}
