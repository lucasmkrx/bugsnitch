// SPDX-License-Identifier: MPL-2.0
package incidents

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitHubDeploymentMonitor(t *testing.T) {
	s := disposableStore(t)
	now := time.Now().UTC().Truncate(time.Second).Add(-15 * time.Minute)
	if signedEvent(s, errorPayload(eventOne, "old", "staging", now.Add(-time.Hour))) != 202 {
		t.Fatal("Sentry fixture rejected")
	}
	if err := s.processAccepted(t.Context()); err != nil {
		t.Fatal(err)
	}
	i := one(t, s)
	candidate, artifact := strings.Repeat("a", 40), strings.Repeat("b", 64)
	if err := s.RecordReceipt(t.Context(), "personal", "p", i.ID, Receipt{CommandID: "original-check", Version: i.Version, Kind: "fix", Candidate: candidate, Evidence: "Shipping threshold regression", At: now}); err != nil {
		t.Fatal(err)
	}

	login := post(s.listener, "/api/v1/login", []byte(`{"token":"isolated-test-configuration-only-owner"}`), map[string]string{"Content-Type": "application/json"})
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	ownerHeaders := map[string]string{"Content-Type": "application/json", "Cookie": login.Result().Cookies()[0].String()}
	base := "/api/v1/projects/p/incidents/" + i.ID
	conf, _ := json.Marshal(MonitorConfig{Version: one(t, s).Version, Candidate: candidate, Environment: "staging", Check: "checkout threshold", Deployer: 10, Verifier: 20, Window: 60, Minimum: 10})
	configured := post(s.listener, base+"/deployment-monitor", conf, ownerHeaders)
	var reg MonitorRegistration
	if configured.Code != 201 || json.Unmarshal(configured.Body.Bytes(), &reg) != nil || reg.ID == "" || reg.Token == "" {
		t.Fatalf("configuration %d %s", configured.Code, configured.Body.String())
	}
	t.Run("probe requires stable serving identity", func(t *testing.T) {
		var reads atomic.Int64
		var moving atomic.Bool
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/shipping" {
				fmt.Fprint(w, "free")
				return
			}
			deployment := 1
			if reads.Add(1) > 1 && moving.Load() {
				deployment = 2
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"candidate": candidate, "build": artifact, "deployment": deployment})
		})
		server := httptest.NewServer(handler)
		defer server.Close()
		run := func() ([]byte, error) {
			return exec.Command("python3", "../../scripts/github-deployment-probe.py", "--monitor", reg.ID, "--name", "checkout threshold", "--manifest", server.URL+"/manifest",
				"--candidate", candidate, "--build", artifact, "--deployment", "1", "--status", "101", "--", "python3", "-c",
				"import sys,json,urllib.request; ok=urllib.request.urlopen(sys.argv[1]).read()==b'free'; print(json.dumps({})); sys.exit(0 if ok else 1)", server.URL+"/shipping").Output()
		}
		out, err := run()
		var body struct {
			Name, Conclusion string
			External         string `json:"external_id"`
		}
		if err != nil || json.Unmarshal(out, &body) != nil || body.Conclusion != "success" || !strings.Contains(body.External, reg.ID) {
			t.Fatalf("probe %v %s", err, out)
		}
		reads.Store(0)
		moving.Store(true)
		if out, err = run(); err == nil || len(out) != 0 {
			t.Fatal("moving deployment produced a check")
		}
	})
	mustExec(t, s.DB, "UPDATE deployment_monitors SET created_at=$1 WHERE id=$2", now, reg.ID)
	type obj = map[string]any
	send := func(event string, delivery int, msg obj, sig string, want int) {
		t.Helper()
		msg["installation"] = obj{"id": 88}
		msg["repository"] = obj{"id": 99}
		b, _ := json.Marshal(msg)
		resp := post(s.listener, "/hooks/github", b, map[string]string{"X-GitHub-Delivery": fmt.Sprintf("00000000-0000-4567-89ab-%012d", delivery), "X-GitHub-Event": event, "X-Hub-Signature-256": "sha256=" + sign(sig, b)})
		if resp.Code != want {
			t.Fatalf("%s HTTP %d: %s", event, resp.Code, resp.Body.String())
		}
	}
	deployment := func(id int64, at time.Time) obj {
		return obj{"action": "created", "deployment": obj{"id": id, "sha": candidate, "creator": obj{"id": 10}, "environment": "staging", "payload": obj{"bugsnitchBuild": artifact}},
			"deployment_status": obj{"id": id + 100, "creator": obj{"id": 10}, "state": "success", "environment": "staging", "created_at": at}}
	}
	check := func(run, app, dep, n int64, at time.Time) obj {
		correlation, _ := json.Marshal(obj{"monitor": reg.ID, "deployment": dep, "status": dep + 100, "build": artifact, "exposures": n, "coverage": true})
		return obj{"action": "completed", "check_run": obj{"id": run, "app": obj{"id": app}, "head_sha": candidate, "name": "checkout threshold", "conclusion": "success", "started_at": at, "completed_at": at.Add(time.Second), "external_id": "bugsnitch/v1 " + string(correlation)}}
	}
	gate := func(token, build string, dep int64, status int, reason string) GateResult {
		t.Helper()
		b, _ := json.Marshal(obj{"candidate": candidate, "build": build, "deployment": dep, "status": dep + 100})
		resp := post(s.listener, "/api/v1/deployment-gates/"+reg.ID, b, map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + token})
		var got GateResult
		_ = json.Unmarshal(resp.Body.Bytes(), &got)
		if resp.Code != status || reason != "" && got.Reason != reason {
			t.Fatalf("gate returned %d %s; expected %d %s", resp.Code, resp.Body.String(), status, reason)
		}
		return got
	}
	send("deployment_status", 1, deployment(1, now.Add(time.Minute)), "wrong-secret", 401)
	send("check_run", 2, check(5, 20, 1, 3, now.Add(3*time.Minute)), "github-fixture", 202)
	gate(reg.Token, artifact, 1, 409, "Awaiting deployment")
	send("deployment_status", 3, deployment(1, now.Add(time.Minute)), "github-fixture", 202)
	gate(reg.Token, artifact, 1, 409, "Verification inconclusive")
	send("check_run", 4, check(6, 21, 1, 12, now.Add(4*time.Minute)), "github-fixture", 202)
	gate(reg.Token, artifact, 1, 409, "Verification inconclusive")
	send("check_run", 5, check(7, 20, 1, 12, now.Add(4*time.Minute)), "github-fixture", 202)
	send("check_run", 5, check(7, 20, 1, 12, now.Add(4*time.Minute)), "github-fixture", 202)
	if !gate(reg.Token, artifact, 1, 200, "Verified in environment").Allowed {
		t.Fatal("gate denied verified build")
	}
	gate(reg.Token, strings.Repeat("c", 64), 1, 409, "Awaiting matching provider evidence")
	send("deployment_status", 6, deployment(2, now.Add(5*time.Minute)), "github-fixture", 202)
	gate(reg.Token, artifact, 2, 409, "Verification inconclusive")
	send("check_run", 7, check(8, 20, 1, 12, now.Add(7*time.Minute)), "github-fixture", 202)
	gate(reg.Token, artifact, 2, 409, "Verification inconclusive")
	send("check_run", 8, check(9, 20, 2, 12, now.Add(7*time.Minute)), "github-fixture", 202)
	// An inactive signal for a previous deployment cannot revoke its successor.
	inactive := deployment(1, now.Add(7*time.Minute))
	inactive["deployment_status"].(obj)["id"] = 211
	inactive["deployment_status"].(obj)["state"] = "inactive"
	send("deployment_status", 15, inactive, "github-fixture", 202)
	if signedEvent(s, errorPayload(strings.Repeat("5", 32), "old", "staging", now.Add(8*time.Minute))) != 202 {
		t.Fatal("old recurrence rejected")
	}
	gate(reg.Token, artifact, 2, 200, "Verified in environment")
	if signedEvent(s, errorPayload(strings.Repeat("4", 32), artifact, "staging", now.Add(9*time.Minute))) != 202 {
		t.Fatal("current recurrence rejected")
	}
	hold := gate(reg.Token, artifact, 2, 409, "current-build recurrence").Hold
	// This worker started before the owner's acknowledgement. An older seen_at
	// on a newly received observation must still open a new hold after the clear.
	waiting, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Rollback()
	var started time.Time
	if err = waiting.QueryRow("SELECT now()").Scan(&started); err != nil {
		t.Fatal(err)
	}
	firstClear, _ := json.Marshal(obj{"holdId": hold})
	if resp := post(s.listener, base+"/deployment-holds/clear", firstClear, ownerHeaders); resp.Code != 200 {
		t.Fatal(resp.Body.String())
	}
	if _, err = waiting.Exec("SELECT id FROM projects WHERE id='p' FOR NO KEY UPDATE"); err != nil {
		t.Fatal(err)
	}
	var delayed int64
	if err = waiting.QueryRow(`INSERT INTO incident_inbox(workspace_id,project_id,source,event_key,group_key,observed,expected,steps,release,environment,frames,seen_at)
           SELECT workspace_id,project_id,source,'delayed-observation',group_key,observed,expected,steps,release,environment,frames,seen_at-interval '10 seconds'
           FROM incident_inbox WHERE event_key=$1 RETURNING id`, installation+"/21/"+strings.Repeat("4", 32)).Scan(&delayed); err != nil {
		t.Fatal(err)
	}
	if err = waiting.Commit(); err != nil {
		t.Fatal(err)
	}
	reopened := gate(reg.Token, artifact, 2, 409, "current-build recurrence").Hold
	if reopened == hold {
		t.Fatal("a receipt accepted after the clear reused the acknowledged hold")
	}
	hold = reopened
	send("deployment_status", 9, deployment(3, now.Add(10*time.Minute)), "github-fixture", 202)
	send("check_run", 10, check(10, 20, 3, 12, now.Add(12*time.Minute)), "github-fixture", 202)
	gate(reg.Token, artifact, 3, 409, "current-build recurrence")
	clear, _ := json.Marshal(obj{"holdId": hold})
	if resp := post(s.listener, base+"/deployment-holds/clear", clear, ownerHeaders); resp.Code != 200 {
		t.Fatal(resp.Body.String())
	}
	gate(reg.Token, artifact, 3, 200, "Verified in environment")
	mustExec(t, s.DB, "UPDATE workspaces SET stopped=true")
	gate(reg.Token, artifact, 3, 409, "Workspace stopped, project paused, or repository disconnected")
	mustExec(t, s.DB, "UPDATE workspaces SET stopped=false")
	rev := check(10, 20, 3, 12, now.Add(12*time.Minute))
	rev["check_run"].(obj)["conclusion"] = "failure"
	send("check_run", 11, rev, "github-fixture", 202)
	revisedHold := gate(reg.Token, artifact, 3, 409, "behavior check failed").Hold
	clear, _ = json.Marshal(obj{"holdId": revisedHold})
	if resp := post(s.listener, base+"/deployment-holds/clear", clear, ownerHeaders); resp.Code != 200 {
		t.Fatal(resp.Body.String())
	}
	gate(reg.Token, artifact, 3, 409, "Verification inconclusive")
	send("check_run", 12, check(10, 20, 3, 12, now.Add(12*time.Minute)), "github-fixture", 202)
	gate(reg.Token, artifact, 3, 409, "Verification inconclusive")
	send("check_run", 13, check(11, 20, 3, 12, now.Add(13*time.Minute)), "github-fixture", 202)
	gate(reg.Token, artifact, 3, 200, "Verified in environment")
	skipped := check(12, 20, 3, 12, now.Add(14*time.Minute))
	skipped["check_run"].(obj)["conclusion"] = "cancelled"
	send("check_run", 14, skipped, "github-fixture", 202)
	gate(reg.Token, artifact, 3, 409, "Verification inconclusive")
	mustExec(t, s.DB, "UPDATE github_mappings SET active=false")
	gate(reg.Token, artifact, 3, 409, "Workspace stopped, project paused, or repository disconnected")
	disabled, _ := json.Marshal(obj{"monitorId": reg.ID})
	if resp := post(s.listener, base+"/deployment-monitor/disable", disabled, ownerHeaders); resp.Code != 200 {
		t.Fatal(resp.Body.String())
	}
	gate(reg.Token, artifact, 3, 403, "")

	t.Run("merge SHA requires independent integrated check", func(t *testing.T) {
		mustExec(t, s.DB, "UPDATE github_mappings SET active=true")
		merged := strings.Repeat("c", 40)
		criteria := strings.Repeat("d", 64)
		mustExec(t, s.DB, `INSERT INTO incident_issues(workspace_id,project_id,incident_id,installation_id,repository_id,marker,approved_version,status,number,issue_id,github_state,requirements_digest,checked_at,next_run)
            VALUES('personal','p',$1,88,99,'monitor-issue',1,'linked',11,555,'closed',$2,now(),now()+interval '1 day')`, i.ID, criteria)
		snap, _ := json.Marshal(obj{"state": "MERGED", "mergedSha": merged, "head": candidate, "target": "main", "criteria": criteria})
		mustExec(t, s.DB, `INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version)
            VALUES('personal','p',22,11,1,$1,1,1)`, string(snap))
		cfg := MonitorConfig{Version: one(t, s).Version, Candidate: candidate, Environment: "staging", Check: "checkout threshold", Deployer: 10, Verifier: 20,
			PullNumber: 22, PullVersion: 1, FixCheck: "integrated threshold", FixApp: 30}
		configure := func(want int) {
			t.Helper()
			b, _ := json.Marshal(cfg)
			resp := post(s.listener, base+"/deployment-monitor", b, ownerHeaders)
			if resp.Code != want {
				t.Fatalf("configure merge %d %s", resp.Code, resp.Body.String())
			}
			if want == 201 {
				if err := json.Unmarshal(resp.Body.Bytes(), &reg); err != nil {
					t.Fatal(err)
				}
			}
		}
		configure(409)
		prior := candidate
		candidate = merged
		cfg.Candidate = merged
		configure(201)
		now = time.Now().UTC().Truncate(time.Second).Add(-3 * time.Minute)
		mustExec(t, s.DB, "UPDATE deployment_monitors SET created_at=$1 WHERE id=$2", now, reg.ID)
		artifact = strings.Repeat("e", 64)
		send("deployment_status", 20, deployment(20, now.Add(time.Minute)), "github-fixture", 202)
		send("check_run", 21, check(22, 20, 20, 10, now.Add(2*time.Minute)), "github-fixture", 202)
		fix := func(delivery, app int, sha string) {
			send("check_run", delivery, obj{"action": "completed", "check_run": obj{"id": delivery, "app": obj{"id": app}, "head_sha": sha,
				"name": "integrated threshold", "conclusion": "success", "completed_at": now.Add(time.Minute), "external_id": "bugsnitch/fix/v1 " + reg.ID}}, "github-fixture", 202)
		}
		fix(22, 30, prior)
		fix(23, 31, merged)
		gate(reg.Token, artifact, 20, 409, "Awaiting integrated fix check on the current issue and merged revision")
		fix(24, 30, merged)
		gate(reg.Token, artifact, 20, 200, "Verified in environment")
		send("check_run", 25, obj{"action": "completed", "check_run": obj{"id": 24, "app": obj{"id": 30}, "head_sha": merged, "name": "integrated threshold", "conclusion": "failure", "completed_at": now.Add(time.Minute), "external_id": "bugsnitch/fix/v1 " + reg.ID}}, "github-fixture", 202)
		gate(reg.Token, artifact, 20, 409, "Verification inconclusive")
		fix(26, 30, merged)
		gate(reg.Token, artifact, 20, 200, "Verified in environment")
		inactive := deployment(20, now.Add(2*time.Minute))
		inactive["deployment_status"].(obj)["id"] = 221
		inactive["deployment_status"].(obj)["state"] = "inactive"
		send("deployment_status", 27, inactive, "github-fixture", 202)
		gate(reg.Token, artifact, 20, 409, "Awaiting deployment")
		send("deployment_status", 28, deployment(20, now.Add(time.Minute)), "github-fixture", 202)
		gate(reg.Token, artifact, 20, 409, "Awaiting deployment")
		mustExec(t, s.DB, "UPDATE incident_issues SET requirements_digest=$1 WHERE incident_id=$2", strings.Repeat("f", 64), i.ID)
		gate(reg.Token, artifact, 20, 409, "Awaiting integrated fix check on the current issue and merged revision")
	})

}
