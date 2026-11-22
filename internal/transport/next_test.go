// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestNextSelectionRespectsPlanScopeCapacityAndOwnership(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	p := owner.post(t, "projects", object{"name": "Selected", "repository": "fixture/selected"}, 201)["id"].(string)
	other := owner.post(t, "projects", object{"name": "Other", "repository": "fixture/other"}, 201)["id"].(string)
	code := owner.post(t, "pairings", object{"name": "Test runner", "projectIds": []string{p}}, 201)["code"].(string)
	token := random(32)
	device := owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)["deviceId"].(string)
	req := func(project string, want int) object {
		t.Helper()
		status, out := owner.req(t, "POST", "/api/v1/device/next", protocol.NextRequest{Protocol: 1, Project: project}, token)
		if strings.Contains(fmt.Sprint(out), "SYNTHETIC_SELECTION_SECRET") {
			t.Fatal("recommendation disclosed a restricted criterion")
		}
		if status != want {
			t.Fatal(status, out)
		}
		return out
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	wantIssue := func(n int) {
		t.Helper()
		out := req(p, 200)
		v, ok := out["selection"].(map[string]any)
		if !ok || v["number"] != float64(n) || v["updatedAt"] != "2026-11-21T10:00:00Z" || v["githubId"] != float64(100+n) {
			t.Fatal(n, out)
		}
	}
	reason := func(v string) {
		t.Helper()
		out := req(p, 200)
		msg, _ := out["reason"].(string)
		if out["selection"] != nil || !strings.Contains(msg, v) {
			t.Fatal(v, out)
		}
	}
	req(other, 403)
	reason("mapping")
	exec("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal',$1,1,2)", p)
	reason("Approve a plan")
	plan := domain.Plan{Title: "Change prices", Milestone: "Test"}
	for _, n := range []int{11, 8, 3, 5, 4} {
		title := fmt.Sprintf("Issue %d", n)
		ticket := domain.Ticket{Number: n, Title: title, Criteria: []string{"Show exact prices"}, Verification: "Run price checks", EstimateHours: 1, Priority: 2}
		if n == 3 {
			ticket.Priority = 0
			ticket.Dependencies = []int{5}
		}
		if n == 4 {
			ticket.Priority = 0
			ticket.Criteria = []string{"Authorization: Bearer SYNTHETIC_SELECTION_SECRET"}
		}
		if n == 5 {
			ticket.Priority = 1
			ticket.Resources = []string{"database"}
		}
		plan.Tickets = append(plan.Tickets, ticket)
		i := github.Issue{Title: title, Body: "Test requirement"}
		exec(`INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,checked_at,selection_revision) VALUES('personal',$1,$2,$3,1,2,$4,$5,'open','current',now(),'2026-11-21T10:00:00Z')`, p, n, 100+n, title, i.RequirementsDigest())
	}
	planID := owner.post(t, "plans", object{"projectId": p, "version": 1, "plan": plan}, 201)["id"].(string)
	owner.post(t, "plans/"+planID+"/approve", object{"version": 2}, 200)
	wantIssue(5)
	wantIssue(5)
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM admission").Scan(&count); err != nil || count != 0 {
		t.Fatal("recommendation claimed work", count, err)
	}
	exec(`INSERT INTO admission(workspace_id,device_id,project_id,attempt_id,issue_number,sequence,operation,base_sha,owns_issue,active,in_turn,resources) VALUES('personal',$1,$2,'previous',90,1,'admit','base',true,true,false,'["database"]')`, device, p)
	wantIssue(8)
	exec("UPDATE planned_issue_state SET checked_at=now()-interval '2 hours' WHERE number=8")
	wantIssue(11)
	exec("INSERT INTO prerequisite_evidence(workspace_id,project_id,plan_id,issue_number,revision,summary) VALUES('personal',$1,$2,5,'candidate','Verified')", p, planID)
	wantIssue(3)
	exec("UPDATE workspaces SET ticket_limit=1")
	reason("implementation slots")
	exec("UPDATE workspaces SET ticket_limit=2,turn_limit=1")
	exec("INSERT INTO general_model_turns(workspace_id,device_id,permit_id,role,version) VALUES('personal',$1,'proof','proof',1)", device)
	reason("model-turn slots")
	exec("UPDATE general_model_turns SET version=2,ended_at=now()")
	exec("UPDATE planned_issue_state SET generation=generation+1 WHERE number=3")
	wantIssue(11)
	exec("UPDATE planned_issue_state SET selection_revision='' WHERE number=11")
	reason("No eligible issue")
	exec("UPDATE projects SET paused=true WHERE id=$1", p)
	reason("paused")
	owner.post(t, "devices/"+device+"/revoke", object{}, 200)
	req(p, 403)
}

// The maximum 64-ticket plan contains 32 candidates sharing 32 completed
// prerequisites. A restricted earlier candidate must still allow the first
// shareable issue through. A missing prerequisite blocks all 32 at once.
func TestNextSelectionMaximumPlan(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	project := owner.post(t, "projects", object{"name": "Batch", "repository": "fixture/batch"}, 201)["id"].(string)
	code := owner.post(t, "pairings", object{"name": "Mac", "projectIds": []string{project}}, 201)["code"].(string)
	token := random(32)
	owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal',$1,1,2)", project)
	exec("INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,checked_at,selection_revision) SELECT 'personal',$1,n,100+n,1,2,'Issue '||n,repeat('e',64),CASE WHEN n<=32 THEN 'closed' ELSE 'open' END,'current',now(),'2026-11-21T11:00:00Z' FROM generate_series(1,64) n", project)
	plan := domain.Plan{Title: "Batch", Milestone: "Final set"}
	var prereqs []int
	for n := 1; n <= 64; n++ {
		ticket := domain.Ticket{Number: n, Title: fmt.Sprintf("Issue %d", n), Criteria: []string{"Prices display correctly"}, Verification: "Run the price test", EstimateHours: 1}
		if n <= 32 {
			prereqs = append(prereqs, n)
			ticket.Priority = 4
		} else {
			ticket.Dependencies = prereqs
			if n < 64 {
				ticket.Criteria = []string{"Authorization: Bearer SYNTHETIC_SELECTION_SECRET"}
			}
		}
		plan.Tickets = append(plan.Tickets, ticket)
	}
	id := owner.post(t, "plans", object{"version": 1, "projectId": project, "plan": plan}, 201)["id"].(string)
	owner.post(t, "plans/"+id+"/approve", object{"version": 2}, 200)
	exec("INSERT INTO prerequisite_evidence(workspace_id,project_id,plan_id,issue_number,revision,summary) SELECT 'personal',$1,$2,n,'commit','Verified' FROM generate_series(1,32) n", project, id)

	next := func() object {
		t.Helper()
		status, body := owner.req(t, "POST", "/api/v1/device/next", protocol.NextRequest{Protocol: 1, Project: project}, token)
		if status != 200 || strings.Contains(fmt.Sprint(body), "SYNTHETIC_SELECTION_SECRET") {
			t.Fatalf("recommendation status=%d; expected a sanitized response", status)
		}
		return body
	}
	out := next()
	if selection, ok := out["selection"].(map[string]any); !ok || selection["number"] != float64(64) || selection["githubId"] != float64(164) {
		t.Fatalf("expected the last shareable candidate, got %v", out)
	}
	exec("UPDATE planned_issue_state SET selection_revision='' WHERE workspace_id='personal' AND project_id=$1 AND number=64", project)
	if body := next(); body["selection"] != nil || body["reason"] != "No eligible issue. Highest-priority issue #33: the approved criteria require redaction before sharing" {
		t.Fatal("sharing boundary", body)
	}
	exec("DELETE FROM prerequisite_evidence WHERE workspace_id='personal' AND project_id=$1 AND plan_id=$2 AND issue_number=32", project, id)
	if body := next(); body["selection"] != nil || body["reason"] != "No eligible issue. Highest-priority issue #33: Waiting for prerequisite integration evidence" {
		t.Fatal("shared prerequisite boundary", body)
	}
}
