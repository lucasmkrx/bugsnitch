// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/device"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

// Exercise the public portal and installed runner with only GitHub and the model simulated.
func TestPortalStartsSelectedTicketFromIntakeThroughOwnerQuestion(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	const repo = "fixture/portal"
	const stamp = "2026-11-21T10:00:00Z"
	project := owner.post(t, "projects", object{"name": "Portal delivery", "repository": repo}, 201)["id"].(string)
	issue := github.Issue{ID: 230, Number: 23, Title: "Use preferred label", Body: "Keep the settings toggle visible", State: "open", UpdatedAt: stamp}
	if _, err := s.DB.Exec("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal',$1,30,31)", project); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,checked_at,selection_revision) VALUES('personal',$1,23,230,30,31,$2,$3,'open','current',now(),$4)`, project, issue.Title, issue.RequirementsDigest(), stamp); err != nil {
		t.Fatal(err)
	}
	code := owner.post(t, "pairings", object{"name": "Paired worker", "projectIds": []string{project}}, 201)["code"].(string)
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	var turns atomic.Int32
	var questions, proposals atomic.Int32
	http.DefaultTransport = routedTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "localhost:8787":
			rec := httptest.NewRecorder()
			if r.URL.Path == "/api/v1/device/progression-wake" {
				r = r.Clone(context.WithoutCancel(r.Context()))
			}
			owner.handler.ServeHTTP(rec, r)
			if rec.Code >= 400 {
				t.Logf("%s returned %d: %s", r.URL.Path, rec.Code, rec.Body.String())
			}
			var response object
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				return nil, err
			}
			if start, ok := response["start"].(map[string]any); ok && start["status"] == "awaiting_plan" && proposals.Add(1) == 1 {
				if turns.Load() != 1 {
					return nil, errors.New("implementation began before plan approval")
				}
				owner.post(t, "plans/"+start["planId"].(string)+"/approve", object{"version": 2}, 200)
			}
			if response["kind"] == "owner_input" && response["question"] == "Which label should be used?" && response["answer"] != "answered" && questions.Add(1) == 1 {
				owner.post(t, "attempts/"+response["attemptId"].(string)+"/ticket-decision", object{"version": response["version"], "answer": "answered", "feedback": "Use the fixed label."}, 200)
			}
			if response["kind"] == "approve_draft" {
				cancel()
			}
			return rec.Result(), nil
		case "api.github.com":
			if r.Method != "GET" || r.URL.Path != "/repos/"+repo+"/issues/23" {
				return nil, fmt.Errorf("unexpected GitHub request %s", r.URL)
			}
			return unattendedResponse(object{"id": issue.ID, "number": issue.Number, "title": issue.Title, "body": issue.Body, "state": issue.State, "updated_at": stamp, "html_url": "https://github.com/" + repo + "/issues/23"})
		case "api.anthropic.com":
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			n := turns.Add(1)
			var answer string
			switch n {
			case 1:
				if !bytes.Contains(payload, []byte(issue.Body)) {
					return nil, errors.New("planner did not receive the issue body")
				}
				doc := domain.Plan{Title: "Plan label change", Milestone: "Verified display", Assumptions: "Verify label preference.", Tickets: []domain.Ticket{{Number: 23, Title: issue.Title, Criteria: []string{"Keep the toggle visible"}, Verification: "Check label", EstimateHours: 2}}}
				encoded, _ := json.Marshal(doc)
				answer = string(encoded)
			case 2:
				answer = `{"summary":"Check required label","disposition":"needs_owner","question":"Which label should be used?"}`
			case 3:
				if !bytes.Contains(payload, []byte("Use the fixed label.")) {
					return nil, errors.New("worker did not receive owner feedback")
				}
				answer = `{"summary":"Updated label","edits":[{"path":"label.txt","content":"fixed\n"}]}`
			case 4:
				answer = `{"summary":"Verified the change","findings":[]}`
			default:
				return nil, errors.New("duplicate model invocation")
			}
			return unattendedResponse(object{"id": fmt.Sprintf("turn-%d", n), "model": "fixture-claude", "stop_reason": "end_turn", "content": []object{{"type": "text", "text": answer}}})
		default:
			return nil, errors.New("unexpected outbound host")
		}
	})
	checkout := setupPairedWorker(t, s, repo, project, code)
	// Resolve the remote main SHA from our fixture checkout, with no real GitHub traffic.
	base, err := exec.Command("git", "-C", checkout, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	fixtureGit := t.TempDir()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \" $* \" in *' ls-remote --refs -- https://github.com/" + repo + ".git refs/heads/main '*) printf '%s\\trefs/heads/main\\n' '" + strings.TrimSpace(string(base)) + "'; exit 0;; esac\nexec '" + realGit + "' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(fixtureGit, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fixtureGit+string(os.PathListSeparator)+os.Getenv("PATH"))
	bind, submit := runner.BindGeneral, runner.SubmitGeneral
	runner.BindGeneral, runner.SubmitGeneral = device.BindGeneral, device.SubmitGeneral
	t.Cleanup(func() { runner.BindGeneral, runner.SubmitGeneral = bind, submit })
	var deviceID string
	if err := s.DB.QueryRow("SELECT id FROM devices WHERE NOT revoked").Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	path := "projects/" + project + "/issues/23/start"
	req := object{"deviceId": deviceID, "policyVersion": 1, "requestId": "portal-req-a", "planId": ""}
	unauth, _ := owner.req(t, "POST", "/api/v1/device/worker-start", object{"protocol": 1, "projectId": project, "operation": "poll"}, strings.Repeat("0", 64))
	if unauth != 403 {
		t.Fatalf("unpaired device received %d", unauth)
	}
	first := owner.post(t, path, req, 202)
	if first["id"] != owner.post(t, path, req, 202)["id"] {
		t.Fatal("replay created another request")
	}
	owner.post(t, path, object{"deviceId": deviceID, "policyVersion": 1, "requestId": "portal-req-b"}, 409)
	var log bytes.Buffer
	if err := device.PortalWorker(ctx, []string{"--project", project}, &log); !errors.Is(err, context.Canceled) {
		t.Fatalf("worker: %v\n%s", err, log.String())
	}
	status, view := owner.req(t, "GET", "/api/v1/projects/"+project+"/worker-starts", nil, "")
	if status != 200 || len(view["starts"].([]any)) != 1 {
		t.Fatal(status, view)
	}
	record := view["starts"].([]any)[0].(map[string]any)
	if record["status"] != "started" || record["attemptId"] != first["id"] || turns.Load() != 4 || questions.Load() != 1 || proposals.Load() != 1 {
		t.Fatal(record, turns.Load(), questions.Load(), proposals.Load(), log.String())
	}
	a, err := runner.Get(first["id"].(string))
	if err != nil || a.State != "completed" || a.CandidateSHA == "" || a.OwnerAnswer != "Use the fixed label." {
		t.Fatal(a.Summary, err)
	}
}

func TestPortalStartBindsCurrentApprovedPlanDeviceAndRevision(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	project := owner.post(t, "projects", object{"name": "Bound work", "repository": "fixture/bound"}, 201)["id"].(string)
	issue := github.Issue{Title: "Fix label", Body: "Keep the existing toggle"}
	if _, err := s.DB.Exec("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal',$1,30,31)", project); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,checked_at,selection_revision)
		VALUES('personal',$1,23,230,30,31,$2,$3,'open','current',now(),'2026-11-21T10:00:00Z')`, project, issue.Title, issue.RequirementsDigest()); err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{Title: "Fix label", Milestone: "Validated", Tickets: []domain.Ticket{{Number: 23, Title: issue.Title, Criteria: []string{"The toggle stays visible"}, Verification: "Check label", EstimateHours: 2}}}
	pid := owner.post(t, "plans", object{"projectId": project, "version": 1, "plan": plan}, 201)["id"].(string)
	owner.post(t, "plans/"+pid+"/approve", object{"version": 2}, 200)
	pair := func() (string, string) {
		t.Helper()
		token := random(32)
		code := owner.post(t, "pairings", object{"name": "Worker", "projectIds": []string{project}}, 201)["code"]
		v := owner.post(t, "device/pair", object{"protocol": 1, "code": code, "credential": token, "platform": "darwin/arm64"}, 200)
		return v["deviceId"].(string), token
	}
	device, token := pair()
	_, other := pair()
	path := "projects/" + project + "/issues/23/start"
	req := object{"requestId": "worker-bound", "deviceId": device, "policyVersion": 1, "planId": pid}
	asked := owner.post(t, path, req, 202)
	poll := func(token string) object {
		t.Helper()
		code, body := owner.req(t, "POST", "/api/v1/device/worker-start", object{"protocol": 1, "projectId": project, "operation": "poll"}, token)
		if code != 200 {
			t.Fatal(code, body)
		}
		return body
	}
	if v := poll(other); v["start"] != nil {
		t.Fatal("different assigned device claimed ticket", v)
	}
	if v := poll(token)["start"].(map[string]any); v["id"] != asked["id"] || v["status"] != "starting" || v["planId"] != pid {
		t.Fatal(v)
	}
	if _, err := s.DB.Exec("UPDATE planned_issue_state SET selection_revision='2026-11-21T12:00:00Z' WHERE project_id=$1", project); err != nil {
		t.Fatal(err)
	}
	if v := poll(token); v["start"] != nil {
		t.Fatal("stale revision redelivered", v)
	}
	if replay := owner.post(t, path, req, 202); replay["status"] != "failed" || replay["id"] != asked["id"] {
		t.Fatal(replay)
	}
	req["planId"] = ""
	owner.post(t, path, req, 409)
}

func TestPortalSerializesIntakeAndPreservesReviewedScope(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	project := owner.post(t, "projects", object{"name": "Shared approval", "repository": "fixture/scope"}, 201)["id"].(string)
	if _, err := s.DB.Exec("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal',$1,3,4)", project); err != nil {
		t.Fatal(err)
	}
	for n := 22; n < 25; n++ {
		if _, err := s.DB.Exec(`INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,checked_at,selection_revision)
			VALUES('personal',$1,$2,100+$2::bigint,3,4,'Issue',$3,'open','current',now(),'2026-11-21T10:00:00Z')`, project, n, strings.Repeat("f", 64)); err != nil {
			t.Fatal(err)
		}
	}
	prior := domain.Plan{Title: "Existing approved deliverable", Milestone: "Owner milestone", Assumptions: "Existing assumption", Tickets: []domain.Ticket{{Number: 22, Title: "Manual ticket", Criteria: []string{"Preserve behavior"}, Verification: "Run checks", EstimateHours: 3, Resources: []string{"label"}}}}
	manual := owner.post(t, "plans", object{"projectId": project, "version": 1, "plan": prior}, 201)["id"].(string)
	start := func(n int) (string, string) {
		t.Helper()
		token := random(32)
		code := owner.post(t, "pairings", object{"name": "Paired worker", "projectIds": []string{project}}, 201)["code"]
		d := owner.post(t, "device/pair", object{"protocol": 1, "code": code, "credential": token, "platform": "darwin/arm64"}, 200)
		v := owner.post(t, fmt.Sprintf("projects/%s/issues/%d/start", project, n), object{"requestId": fmt.Sprintf("request-%d", n), "deviceId": d["deviceId"], "policyVersion": 1, "planId": ""}, 202)
		return v["id"].(string), token
	}
	first, token := start(23)
	_, other := start(24)
	exchange := func(token string, body object) map[string]any {
		t.Helper()
		body["projectId"] = project
		body["protocol"] = 1
		code, v := owner.req(t, "POST", "/api/v1/device/worker-start", body, token)
		if code != 200 {
			t.Fatal(code, v)
		}
		item, _ := v["start"].(map[string]any)
		return item
	}
	poll := func(token string) map[string]any { return exchange(token, object{"operation": "poll"}) }
	if poll(token) != nil || poll(other) != nil {
		t.Fatal("runner took work ahead of the pending manual proposal")
	}
	owner.post(t, "plans/"+manual+"/approve", object{"version": 2}, 200)
	owner.post(t, "plans/"+manual+"/evidence", object{"number": 22, "revision": strings.Repeat("a", 40), "summary": "Verified earlier ticket"}, 201)
	if v := poll(token); v["status"] != "planning" || v["id"] != first {
		t.Fatal(v)
	}
	if v := poll(other); v != nil {
		t.Fatal("second runner started a competing proposal", v)
	}
	addition := domain.Plan{Title: "New work", Milestone: "Validated", Assumptions: "New estimate assumes coverage", Tickets: []domain.Ticket{{Number: 23, Title: "New ticket", Criteria: []string{"Show label"}, Verification: "Check label", EstimateHours: 2}}}
	withPlan := exchange(token, object{"operation": "proposal", "id": first, "proposal": addition})
	nextID := withPlan["planId"].(string)
	if withPlan["status"] != "awaiting_plan" || poll(other) != nil {
		t.Fatal(withPlan)
	}
	var stored domain.Plan
	var data []byte
	if err := s.DB.QueryRow("SELECT document FROM plan_versions WHERE id=$1", nextID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stored); err != nil || len(stored.Tickets) != 2 || stored.Tickets[0].Resources[0] != "label" || stored.Tickets[1].Number != 23 || stored.Milestone != prior.Milestone || !strings.Contains(stored.Assumptions, addition.Assumptions) {
		t.Fatal(string(data), err)
	}
	var bindings int
	if err := s.DB.QueryRow("SELECT count(*) FROM plan_issue_bindings WHERE plan_id=$1", nextID).Scan(&bindings); err != nil || bindings != 2 {
		t.Fatal(bindings, err)
	}
	owner.post(t, "plans/"+nextID+"/approve", object{"version": 4}, 200)
	if v := poll(token); v["status"] != "starting" {
		t.Fatal(v)
	}
	if poll(other) != nil {
		t.Fatal("second runner replaced the first ticket before admission")
	}
	if _, err := s.DB.Exec(`INSERT INTO admission(workspace_id,project_id,device_id,attempt_id,issue_number,base_sha,sequence,operation,owns_issue,active,in_turn,plan_id)
		SELECT workspace_id,project_id,device_id,id,issue_number,'base',1,'admit',true,true,false,plan_id FROM worker_starts WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	var carried string
	if err := s.DB.QueryRow("SELECT revision FROM prerequisite_evidence WHERE plan_id=$1 AND issue_number=22", nextID).Scan(&carried); err != nil || carried != strings.Repeat("a", 40) {
		t.Fatal(carried, err)
	}
	if v := poll(other); v["status"] != "planning" {
		t.Fatal(v)
	}
}
