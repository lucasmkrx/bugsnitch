// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceMemberPermissionsAttributionAndRevocation(t *testing.T) {
	server := fixture(t)
	owner := login(t, server)
	project := owner.post(t, "projects", object{"name": "Project", "repository": "example/work"}, 201)["id"].(string)
	operator := owner.post(t, "members", object{"name": "Alex", "role": "operator"}, 201)
	approver := owner.post(t, "members", object{"name": "Casey", "role": "approver"}, 201)
	signIn := func(member object, want int) system {
		t.Helper()
		body, _ := json.Marshal(object{"token": member["credential"]})
		req := httptest.NewRequest("POST", "/api/v1/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("login returned %d, want %d: %s", w.Code, want, w.Body)
		}
		if want == 200 {
			return system{handler: server.handler, cookie: w.Result().Cookies()[0]}
		}
		return system{}
	}
	op, app := signIn(operator, 200), signIn(approver, 200)
	if code, state := op.req(t, "GET", "/api/v1/state", nil, ""); code != 200 || state["viewer"].(map[string]any)["name"] != "Alex" {
		t.Fatalf("operator state: %d %v", code, state["viewer"])
	}
	report := object{"projectId": project, "observed": "Wrong total", "expected": "Right total", "steps": "Open checkout", "requestId": "operator_report"}
	op.post(t, "incidents", report, 201)
	op.post(t, "projects", object{"name": "Denied", "repository": "example/other"}, 403)
	op.post(t, "members", object{"name": "Denied", "role": "approver"}, 403)
	app.post(t, "pairings", object{"name": "Denied", "projectIds": []string{project}}, 403)
	app.post(t, "workspace/dispatch", object{"stopped": true, "version": 1, "requestId": "denied_stop"}, 403)
	owner.post(t, "projects/"+project+"/dispatch", object{"paused": true, "version": 1}, 200)
	decision := owner.post(t, "projects/"+project+"/dispatch", object{"paused": false, "version": 2}, 202)["decisionId"].(string)
	answer := object{"answer": "Resume dispatch", "version": 1, "requestId": "member_answer"}
	op.post(t, "decisions/"+decision+"/answer", answer, 403)
	app.post(t, "decisions/"+decision+"/answer", answer, 200)

	var attribution int
	err := server.DB.QueryRow(`SELECT count(*) FROM events WHERE
		(kind='incident.observed' AND actor_source='operator' AND actor_id=$1)
		OR (kind='decision.answered' AND actor_source='approver' AND actor_id=$2)`,
		operator["id"], approver["id"]).Scan(&attribution)
	if err != nil || attribution != 2 {
		t.Fatalf("member attribution = %d, %v", attribution, err)
	}
	owner.post(t, "members/"+approver["id"].(string)+"/revoke", object{}, 200)
	if code, _ := app.req(t, "GET", "/api/v1/state", nil, ""); code != 401 {
		t.Fatal("revoked session could still read state", code)
	}
	signIn(approver, 401)
	op.post(t, "logout", object{}, 200)
	if code, _ := op.req(t, "GET", "/api/v1/state", nil, ""); code != 401 {
		t.Fatal("member logout did not invalidate session", code)
	}
}

func TestMemberProjectScope(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	add := func(name string) string {
		return owner.post(t, "projects", object{"name": name, "repository": "example/" + name}, 201)["id"].(string)
	}
	visible, hidden := add("visible"), add("hidden")
	var stages []releaseStage
	head := strings.Repeat("a", 40)
	for i, p := range []string{visible, hidden} {
		if _, err := s.DB.Exec("INSERT INTO github_mappings VALUES('personal',$1,2,$2,true)", p, i+5); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version)
            VALUES('personal',$1,10,9,1,jsonb_build_object('state','OPEN','head',$2::text,'target','main'),1,0)`, p, head); err != nil {
			t.Fatal(err)
		}
		stages = append(stages, releaseStage{p, 10, head, "main"})
	}
	owner.post(t, "releases", object{"name": "private rollout", "requestId": "private_rollout_id", "environment": "staging", "stages": stages}, 201)
	if _, err := s.DB.Exec("UPDATE workspaces SET monthly_turn_limit=796351 WHERE id='personal'"); err != nil {
		t.Fatal(err)
	}
	code, ownerState := owner.req(t, "GET", "/api/v1/state", nil, "")
	if code != 200 || len(ownerState["releases"].([]any)) != 1 || ownerState["budget"].(map[string]any)["monthlyTurns"] != float64(796351) {
		t.Fatal("owner release or budget missing", code)
	}

	member := owner.post(t, "members", object{"name": "one-project", "role": "approver", "projectId": visible}, 201)
	s.OwnerToken = member["credential"].(string)
	scoped := login(t, s)
	report := func(p string) object {
		return object{"projectId": p, "observed": "Broken", "expected": "Pass", "steps": "Open", "requestId": "report_fixture_key"}
	}
	scoped.post(t, "incidents", report(hidden), 403)
	scoped.post(t, "incidents", report(visible), 201)
	owner.post(t, "projects/"+hidden+"/dispatch", object{"paused": true, "version": 1}, 200)
	decision := owner.post(t, "projects/"+hidden+"/dispatch", object{"paused": false, "version": 2}, 202)["decisionId"].(string)
	scoped.post(t, "decisions/"+decision+"/answer", object{"answer": "Resume dispatch", "version": 1, "requestId": "deny_other_project"}, 403)
	code, state := scoped.req(t, "GET", "/api/v1/state", nil, "")
	raw, _ := json.Marshal(state)
	if code != 200 || state["budget"] != nil || len(state["releases"].([]any)) != 0 || len(state["pullRequests"].([]any)) != 1 || bytes.Contains(raw, []byte("private_rollout_id")) || bytes.Contains(raw, []byte(hidden)) || !bytes.Contains(raw, []byte(visible)) || len(state["projects"].([]any)) != 1 {
		t.Fatalf("scope boundary failed: %d %s", code, raw)
	}
	for _, path := range []string{"/api/v1/events", "/api/v1/attempts/private-attempt/diff", "/api/v1/diagnostics", "/api/v1/projects/" + hidden + "/incidents/private-incident/verification?environment=staging"} {
		if status, _ := scoped.req(t, "GET", path, nil, ""); status != 401 && status != 403 {
			t.Fatalf("scope accepted %s: %d", path, status)
		}
	}
}
