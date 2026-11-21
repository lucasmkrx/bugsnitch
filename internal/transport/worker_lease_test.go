// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestWorkerLeaseChecksOriginalSubjectAndCurrentAuthority(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	pr := owner.post(t, "projects", object{"name": "Local execution", "repository": "fixture/worker"}, 201)["id"].(string)
	pair := func() (string, string) {
		code := owner.post(t, "pairings", object{"name": "Test device", "projectIds": []string{pr}}, 201)["code"].(string)
		token := random(32)
		id := owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)["deviceId"].(string)
		return id, token
	}
	d, token := pair()
	_, foreign := pair()
	a := protocol.WorkerLease{Protocol: 1, Workspace: "personal", Project: pr, Device: d, Attempt: "fixture_worker_lease", Base: strings.Repeat("d", 40), Issue: 7, Sequence: 1, SubjectVersion: 2}
	check := func(cred string, want int) object {
		t.Helper()
		status, out := owner.req(t, "POST", "/api/v1/device/worker-lease", a, cred)
		if status != want {
			t.Fatalf("grant %d want %d: %+v", status, want, out)
		}
		return out
	}
	check(token, 403)
	admission := func(seq int64, op string) {
		t.Helper()
		status, out := owner.req(t, "POST", "/api/v1/device/admission", protocol.Admission{Protocol: 1, ProjectID: pr, AttemptID: a.Attempt, IssueNumber: a.Issue, BaseSHA: a.Base, Operation: op, Sequence: seq}, token)
		if status != 200 {
			t.Fatal(op, status, out)
		}
	}
	admission(1, "admit")
	if out := check(token, 200); out["validSeconds"] != float64(protocol.WorkerLeaseSeconds) {
		t.Fatal(out)
	}
	check(foreign, 403)
	a.Workspace = "foreign"
	check(token, 403)
	a.Workspace = "personal"
	a.Sequence = 2
	check(token, 409)
	a.Turn = true
	admission(2, "turn_start")
	check(token, 200)
	a.Sequence = 1
	check(token, 409)
	a.Sequence = 2
	a.Turn = false
	check(token, 409)
	a.Turn = true
	report := protocol.Attempt{ID: a.Attempt, ProjectID: pr, IssueNumber: a.Issue, BaseSHA: a.Base, State: "running", Phase: "plan", Version: 2}
	status, out := owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Attempts: []protocol.Attempt{report}}, token)
	if status != 200 {
		t.Fatal(status, out)
	}
	badReason := report
	badReason.ReasonCode = "SYNTHETIC_SECRET"
	if c, _ := owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Attempts: []protocol.Attempt{badReason}}, token); c != 400 {
		t.Fatal("arbitrary reason accepted", c)
	}
	badReason.ReasonCode = "provider_rate_limited"
	if c, _ := owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Attempts: []protocol.Attempt{badReason}}, token); c != 409 {
		t.Fatal("same-version report changed reason", c)
	}
	command := owner.post(t, "attempts/"+a.Attempt+"/control", object{"operation": "stop", "requestId": "stop_fixture_worker", "version": 2}, 202)
	if grant := check(token, 200); grant["validSeconds"] != float64(0) || grant["control"].(map[string]any)["id"] != command["id"] {
		t.Fatal("received new authority while stop pending", grant, command)
	}
	a.SubjectVersion = 3
	if _, err := s.DB.Exec("UPDATE workspaces SET stopped=true WHERE id='personal'"); err != nil {
		t.Fatal(err)
	}
	check(token, 409)
	owner.post(t, "devices/"+d+"/revoke", object{}, 200)
	check(token, 403)
}
