// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestPortalAnswerIsPrivateAndBoundToWaitingCheckpoint(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	project := owner.post(t, "projects", object{"name": "Portal", "repository": "example/portal"}, 201)["id"].(string)
	token := random(32)
	code := owner.post(t, "pairings", object{"name": "Paired runner", "projectIds": []string{project}}, 201)["code"].(string)
	device := owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)["deviceId"].(string)
	id := strings.Repeat("e", 32)
	report := protocol.Attempt{ProjectID: project, ID: id, IssueNumber: 1, BaseSHA: strings.Repeat("a", 40), Phase: "implement", State: "waiting", ReasonCode: protocol.OwnerInputRequired, Version: 2}
	plan := domain.Plan{Title: "Correct label", Milestone: "Receipt", Tickets: []domain.Ticket{{Number: 1, Title: "Correct label", Criteria: []string{"Show the selected label"}, Verification: "Check label", EstimateHours: 1}}}
	planID := owner.post(t, "plans", object{"projectId": report.ProjectID, "version": 1, "plan": plan}, 201)["id"].(string)
	owner.post(t, "plans/"+planID+"/approve", object{"version": 2}, 200)
	if _, err := s.DB.Exec(`INSERT INTO admission(workspace_id,project_id,device_id,attempt_id,issue_number,base_sha,sequence,operation,active,in_turn,plan_id)
	    VALUES('personal',$1,$2,$3,1,$4,1,'admit',true,false,$5)`, project, device, id, report.BaseSHA, planID); err != nil {
		t.Fatal(err)
	}
	if code, b := owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Attempts: []protocol.Attempt{report}}, token); code != 200 {
		t.Fatal(code, b)
	}
	request := object{"protocol": 1, "attemptId": id, "version": 2, "question": "Which label should appear?"}
	call := func(token string, want int) object {
		t.Helper()
		code, b := owner.req(t, "POST", "/api/v1/device/progression", request, token)
		if code != want {
			t.Fatalf("device progression %d: %v", code, b)
		}
		return b
	}
	if a := call(token, 200); a["kind"] != "owner_input" || a["question"] != request["question"] {
		t.Fatal(a)
	}
	answer := object{"version": 2, "answer": "answered", "feedback": "Use the customer-facing label."}
	path := "attempts/" + id + "/ticket-decision"
	owner.post(t, path, object{"version": 2, "answer": "approve"}, 400)
	owner.post(t, path, answer, 200)
	owner.post(t, path, answer, 200)
	owner.post(t, path, object{"version": 2, "answer": "answered", "feedback": "Overwrite"}, 409)
	delete(request, "question")
	if a := call(token, 200); a["feedback"] != answer["feedback"] || a["answer"] != "answered" {
		t.Fatal(a)
	}
	call(random(32), 403)
	var public, leaked bool
	err := s.DB.QueryRow(`SELECT (SELECT action ? 'feedback' FROM attempt_progression WHERE id=$1),
	     EXISTS(SELECT FROM events WHERE reason=$2)`, id, answer["feedback"]).Scan(&public, &leaked)
	if err != nil || public || leaked {
		t.Fatal("private answer reached public projection or audit", err)
	}
	if _, err = s.DB.Exec("UPDATE projects SET policy_version=policy_version+1 WHERE id=$1", report.ProjectID); err != nil {
		t.Fatal(err)
	}
	if a := call(token, 200); a["feedback"] != nil || a["answer"] != nil {
		t.Fatal("stale answer returned", a)
	}
	owner.post(t, path, answer, 409)
}
