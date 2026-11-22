// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

// A persisted admission from before the Rust cutover can resume on its
// original device with the criteria digest already in the local journal.
func TestNativeClaimsResumeStoredWaitingAttempt(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	project := owner.post(t, "projects", object{"name": "Archive", "repository": "example/archive"}, 201)["id"].(string)
	code := owner.post(t, "pairings", object{"name": "Mac", "projectIds": []string{project}}, 201)["code"].(string)
	token := random(32)
	device := owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)["deviceId"].(string)
	criteria := []string{"Render <title> & café\u2028line\u2029end", "Read \"quoted\" identifiers and a literal \\u0026"}
	ticket := domain.Ticket{Number: 4, Title: "Export report", Criteria: criteria, Verification: "Check report contents", EstimateHours: 1}
	plan := domain.Plan{Title: "Reports", Milestone: "First export", Tickets: []domain.Ticket{ticket}}
	planID := owner.post(t, "plans", object{"projectId": project, "version": 1, "plan": plan}, 201)["id"].(string)
	owner.post(t, "plans/"+planID+"/approve", object{"version": 2}, 200)
	a := protocol.Admission{Protocol: 1, ProjectID: project, IssueNumber: 4, AttemptID: "preexisting_worker", BaseSHA: strings.Repeat("c", 40), Sequence: 12, Operation: "admit", CriteriaHash: domain.CriteriaHash(criteria)}
	_, err := s.DB.Exec("INSERT INTO admission(workspace_id,project_id,device_id,attempt_id,issue_number,base_sha,sequence,operation,active,in_turn,owns_issue,plan_id) VALUES('personal',$1,$2,$3,4,$4,11,'finish',false,false,true,$5)", project, device, a.AttemptID, a.BaseSHA, planID)
	if err != nil {
		t.Fatal(err)
	}
	send := func(want int) {
		t.Helper()
		got, receipt := owner.req(t, "POST", "/api/v1/device/admission", a, token)
		if got != want || got == 200 && receipt["sequence"] != float64(a.Sequence) {
			t.Fatalf("operation %s seq %d: got %d %v, want %d", a.Operation, a.Sequence, got, receipt, want)
		}
	}
	a.CriteriaHash = domain.CriteriaHash([]string{criteria[1], criteria[0]})
	send(409)
	a.CriteriaHash = domain.CriteriaHash(criteria)
	send(200)
	send(200) // Lost resume acknowledgement.
	a.Operation, a.Sequence, a.State = "finish", 13, "completed"
	send(200)
	send(200) // Lost terminal acknowledgement.
	a.State = "waiting"
	send(409)
	a.Operation, a.Sequence, a.State = "admit", 14, ""
	send(409)
	var count int
	err = s.DB.QueryRow("SELECT count(*) FROM events WHERE workspace_id='personal' AND project_id=$1 AND subject_id=$2 AND actor_source='device' AND actor_id=$3 AND evidence_ref=$4 AND subject_version IN (12,13)", project, a.AttemptID, device, a.BaseSHA).Scan(&count)
	if err != nil || count != 2 {
		t.Fatalf("want one attributed event per accepted transition: %d %v", count, err)
	}
	var owned, active bool
	var sequence int64
	err = s.DB.QueryRow("SELECT sequence, owns_issue,active FROM admission WHERE workspace_id='personal' AND attempt_id=$1", a.AttemptID).Scan(&sequence, &owned, &active)
	if err != nil || sequence != 13 || owned || active {
		t.Fatalf("terminal ownership after replay: sequence %d, owned %t, active %t, err %v", sequence, owned, active, err)
	}
}

// Force the claim to own the device row while emergency stop owns the
// workspace. Creating the stop command checks its device foreign key. Both
// operations must finish, with the stopped workspace refusing new work.
func TestNativeClaimDuringEmergencyStop(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	p := owner.post(t, "projects", object{"name": "Stop", "repository": "example/stop"}, 201)["id"].(string)
	code := owner.post(t, "pairings", object{"name": "Mac", "projectIds": []string{p}}, 201)["code"].(string)
	token := random(32)
	owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)
	a := protocol.Admission{Protocol: 1, ProjectID: p, IssueNumber: 1, AttemptID: "running_attempt", BaseSHA: strings.Repeat("a", 40), Sequence: 1, Operation: "admit"}
	post := func(path string, payload any) int {
		status, _ := owner.req(t, "POST", "/api/v1/device/"+path, payload, token)
		return status
	}
	if post("admission", a) != 200 {
		t.Fatal("could not admit first attempt")
	}
	running := protocol.Attempt{ID: a.AttemptID, ProjectID: p, IssueNumber: 1, BaseSHA: a.BaseSHA, Version: 1, Phase: "implement", State: "running"}
	if post("sync", protocol.Sync{Protocol: 1, Attempts: []protocol.Attempt{running}}) != 200 {
		t.Fatal("could not report running attempt")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gate, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	var gatePID int
	if err = gate.QueryRowContext(ctx, "SELECT pg_backend_pid() FROM projects WHERE id=$1 FOR UPDATE", p).Scan(&gatePID); err != nil {
		t.Fatal(err)
	}
	stopResult := make(chan int, 1)
	go func() {
		status, _ := owner.req(t, "POST", "/api/v1/workspace/dispatch", object{"version": 1, "requestId": "stop_in_flight", "stopped": true}, "")
		stopResult <- status
	}()
	stopPID := waitForBlockedWriter(t, ctx, s.DB, gatePID)
	claimResult := make(chan int, 1)
	go func() {
		a.IssueNumber, a.AttemptID = 2, "concurrent_claim"
		claimResult <- post("admission", a)
	}()
	waitForBlockedWriter(t, ctx, s.DB, stopPID)
	if err = gate.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, op := range []struct {
		name   string
		result <-chan int
		want   int
	}{{"stop", stopResult, 200}, {"claim", claimResult, 409}} {
		select {
		case status := <-op.result:
			if status != op.want {
				t.Fatalf("%s returned %d, want %d", op.name, status, op.want)
			}
		case <-ctx.Done():
			t.Fatalf("%s did not complete", op.name)
		}
	}
	var commands, claims int
	err = s.DB.QueryRow("SELECT (SELECT count(*) FROM controls WHERE project_id=$1 AND attempt_id=$2 AND operation='stop'),(SELECT count(*) FROM admission WHERE project_id=$1)", p, running.ID).Scan(&commands, &claims)
	if err != nil || commands != 1 || claims != 1 {
		t.Fatalf("commands %d, claims %d, err %v", commands, claims, err)
	}
}

func waitForBlockedWriter(t *testing.T, ctx context.Context, db *sql.DB, blocker int) int {
	t.Helper()
	for {
		var pid int
		err := db.QueryRowContext(ctx, "SELECT COALESCE(max(pid),0) FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid))", blocker).Scan(&pid)
		if err != nil {
			t.Fatal(err)
		}
		if pid > 0 {
			return pid
		}
		select {
		case <-ctx.Done():
			t.Fatal("database writer did not reach lock barrier")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
