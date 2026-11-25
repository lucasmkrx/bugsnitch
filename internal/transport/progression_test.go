// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
	"github.com/lucasmkrx/bugsnitch/internal/testserver"
)

func TestUnattendedDeliveryFencesPublicationAndPreparedEvidence(t *testing.T) {
	const secret = "isolated-pull-watch-fixture"
	s := fixtureWithIntegrations(t, testserver.Config{GitHubSecret: secret})
	owner := login(t, s)
	p := owner.post(t, "projects", object{"name": "Delivery", "repository": "fixture/delivery"}, 201)["id"].(string)
	token := random(32)
	code := owner.post(t, "pairings", object{"name": "Delivery runner", "projectIds": []string{p}}, 201)["code"].(string)
	device := owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)["deviceId"].(string)
	sql := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	sql("UPDATE projects SET allow_draft_pr=true WHERE id=$1", p)
	sql("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal',$1,20,25)", p)
	plan := domain.Plan{Title: "Update checkout", Milestone: "Receipt", Tickets: []domain.Ticket{{Number: 5, Title: "Fix total", Criteria: []string{"Display the correct total"}, Verification: "Check total", EstimateHours: 1}}}
	planID := owner.post(t, "plans", object{"projectId": p, "version": 1, "plan": plan}, 201)["id"].(string)
	owner.post(t, "plans/"+planID+"/approve", object{"version": 2}, 200)
	var policy int64
	if err := s.DB.QueryRow("SELECT policy_version FROM projects WHERE id=$1", p).Scan(&policy); err != nil {
		t.Fatal(err)
	}
	root, correction := strings.Repeat("1", 32), strings.Repeat("2", 32)
	base, previous, current := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	for id, head := range map[string]string{root: previous, correction: current} {
		sql(`INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version)
		 VALUES($1,'personal',$2,$3,5,$4,$5,'wrap_up','completed',3)`, id, p, device, base, head)
	}
	sql(`INSERT INTO draft_pr_intents(workspace_id,project_id,attempt_id,report_version,policy_version,installation_id,repository_id,issue_number,issue_updated_at,candidate_sha,base_sha,target,marker,status,local_ship)
	  VALUES('personal',$1,$2,3,$3,20,25,5,'2026-11-21T10:00:00Z',$4,$5,'main','marker','awaiting_publication',true)`, p, root, policy, previous, base)
	req := func(path string, b object, expected int) object {
		t.Helper()
		got, data := owner.req(t, "POST", "/api/v1/device/"+path, b, token)
		if got != expected {
			t.Fatalf("%s returned %d: %v", path, got, data)
		}
		return data
	}
	if n := req("progression", object{"protocol": 1, "attemptId": root, "version": 3}, 200); n["kind"] != "publish_draft" {
		t.Fatal(n)
	}
	request := object{"protocol": 1, "attemptId": root, "version": 3, "baseSha": base, "candidateSha": previous, "target": "main", "operation": "published", "approvalVersion": 1, "follow": true}
	for range 2 {
		if r := req("draft-delivery", request, 200); r["status"] != "queued" {
			t.Fatal(r)
		}
	}
	sql("UPDATE draft_pr_intents SET status='created',number=12 WHERE attempt_id=$1", root)
	job, err := storage.ClaimJob(t.Context(), s.DB, "pull_watch")
	if err != nil || job.Item != root || job.Number != 12 {
		t.Fatal(job, err)
	}
	sql(`INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version,watching)
	  VALUES('personal',$1,12,5,1,jsonb_build_object('head',$2::text,'base',$3::text,'target','main','state','OPEN','checks','FAILURE'),$4,1,true)`, p, previous, base, policy)
	sql("UPDATE pull_observations SET next_watch=now()+interval '2 minutes' WHERE project_id=$1", p)
	body := `{"installation":{"id":20},"repository":{"id":25},"pull_request":{"number":12}}`
	signature := hmac.New(sha256.New, []byte(secret))
	_, _ = signature.Write([]byte(body))
	for range 2 {
		hook := httptest.NewRequest("POST", "/hooks/github", strings.NewReader(body))
		hook.Header.Set("X-GitHub-Event", "pull_request_review_comment")
		hook.Header.Set("X-GitHub-Delivery", "11111111-1111-4111-8111-111111111111")
		hook.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(signature.Sum(nil)))
		res := httptest.NewRecorder()
		owner.handler.ServeHTTP(res, hook)
		if res.Code != 202 {
			t.Fatal(res.Code, res.Body.String())
		}
	}
	var signals int
	var advanced bool
	if err := s.DB.QueryRow("SELECT progression_signal,next_watch<now()+interval '10 seconds' FROM pull_observations WHERE project_id=$1", p).
		Scan(&signals, &advanced); err != nil || signals != 1 || !advanced {
		t.Fatal(signals, advanced, err)
	}
	subject := object{"rootAttempt": root, "pullNumber": 12, "previousHead": previous, "target": "main"}
	c := object{"protocol": 1, "attemptId": correction, "version": 3, "correction": subject}
	if n := req("progression", c, 200); n["kind"] != "approve_correction" {
		t.Fatal(n)
	}
	answer := "attempts/" + correction + "/ticket-decision"
	owner.post(t, answer, object{"version": 3, "answer": "approve"}, 200)
	c = object{"protocol": 1, "attemptId": correction, "version": 3}
	if n := req("progression", c, 200); n["kind"] != "publish_correction" {
		t.Fatal(n)
	}
	sql("UPDATE pull_observations SET snapshot=jsonb_set(snapshot,'{head}',to_jsonb($1::text)),version=version+1 WHERE project_id=$2", strings.Repeat("d", 40), p)
	if n := req("progression", c, 200); n["kind"] != "inspect_revision" {
		t.Fatal(n)
	}
	owner.post(t, answer, object{"version": 3, "answer": "approve"}, 409)
	sql("UPDATE pull_observations SET snapshot=jsonb_set(snapshot,'{head}',to_jsonb($1::text)) WHERE project_id=$2", previous, p)
	if n := req("progression", c, 200); n["kind"] != "hold" {
		t.Fatal("old approval revived", n)
	}
	sql("UPDATE pull_observations SET snapshot=jsonb_set(jsonb_set(snapshot,'{head}',to_jsonb($1::text)),'{checks}','\"SUCCESS\"'),version=version+1 WHERE project_id=$2", current, p)
	c["prepared"] = object{"pullNumber": 12, "inspectionVersion": 3, "baseSha": base, "independentReview": strings.Repeat("e", 64), "combinedValidation": strings.Repeat("f", 64)}
	if n := req("progression", c, 200); n["kind"] != "prepare_signoff" || n["preparedEvidence"] == nil {
		t.Fatal(n)
	}
	sql("UPDATE pull_observations SET version=version+1 WHERE project_id=$1", p)
	delete(c, "prepared")
	if n := req("progression", c, 200); n["preparedEvidence"] != nil {
		t.Fatal("stale evidence survived revision", n)
	}
}
