// SPDX-License-Identifier: MPL-2.0
package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/testgithub"
)

func TestProtectedQueueReconcilesAndChecksSignedGroup(t *testing.T) {
	db, sqlRun := scratchDB(t)
	type obj = map[string]any
	ctx := audit.WithActor(t.Context(), audit.Owner, "owner")
	id, head, base, group := strings.Repeat("1", 32), strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	t.Setenv("BUGSNITCH_GITHUB_QUEUE_REPOSITORIES", "11")
	const updated = "2026-11-01T12:00:00Z"
	const guard = "Bugsnitch queue authorization"
	groupRef := "gh-readonly-queue/main/pr-18-fixture"
	sqlRun(`INSERT INTO devices(id,workspace_id,name,platform) VALUES('d','personal','fixture','test');INSERT INTO device_projects(workspace_id,device_id,project_id) VALUES('personal','d','p')`)
	stages, _ := json.Marshal([]obj{{"phase": "validate", "candidateSha": head, "exitCode": 0}, {"phase": "review", "candidateSha": head, "importantFindings": 0}})
	sqlRun(`INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version,evidence,issue_updated_at)
		VALUES($1,'personal','p','d',17,$2,$3,'wrap_up','completed',5,$4,$5)`, id, base, head, string(stages), updated)
	sqlRun(`INSERT INTO draft_pr_intents(workspace_id,project_id,attempt_id,report_version,policy_version,installation_id,repository_id,issue_number,issue_updated_at,candidate_sha,base_sha,target,marker,status,number)
	 VALUES('personal','p',$1,5,1,10,11,17,$2,$3,$4,'main','fixture','created',18)`, id, updated, head, base)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	file := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	var guarded, enqueued, multiple, retarget, localOnly atomic.Bool
	var enqueues, groupChecks, headChecks atomic.Int32
	checks := map[string]obj{}
	rules := []obj{{"type": "merge_queue", "parameters": obj{"merge_method": "MERGE"}}, {"type": "required_status_checks", "parameters": obj{"required_status_checks": []obj{{"context": guard, "integration_id": 9}, {"context": "compile", "integration_id": 7}}}}}
	targetRules := []obj{{"type": "merge_queue", "parameters": obj{"merge_method": "MERGE"}}, {"type": "required_status_checks", "parameters": obj{"required_status_checks": []obj{{"context": "Bugsnitch queue / main", "integration_id": 9}, {"context": "compile", "integration_id": 7}}}}}
	origin := testgithub.Serve(t, func(r *http.Request) (*http.Response, error) {
		var val any
		status := 200
		switch r.URL.Path {
		case "/app":
			val = obj{"id": 9, "slug": "fixture"}
		case "/app/installations/10/access_tokens":
			var b struct {
				Permissions map[string]string
				IDs         []int64 `json:"repository_ids"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			if len(b.IDs) != 1 || b.IDs[0] != 11 || (b.Permissions["contents"] != "read" && !(b.Permissions["administration"] == "write" && len(b.Permissions) == 1)) {
				t.Errorf("unexpected queue scope %+v", b)
			}
			val = obj{"token": "fixture"}
			status = 201
		case "/repositories/11":
			val = obj{"id": 11, "full_name": "example/test"}
		case "/repos/example/test/rules/branches/main", "/repos/example/test/rules/branches/production":
			val = rules
			if localOnly.Load() {
				val = targetRules
			}
		case "/repos/example/test/rulesets":
			val = []obj{{"id": 3, "enforcement": "active", "target": "branch"}}
		case "/repos/example/test/rulesets/3":
			v := obj{"target": "branch", "enforcement": "active", "source_type": "Repository", "source": "example/test", "bypass_actors": []obj{}, "conditions": obj{"ref_name": obj{"include": []string{"~ALL"}, "exclude": []string{}}}, "rules": rules}
			if localOnly.Load() {
				v["rules"] = targetRules
				v["conditions"] = obj{"ref_name": obj{"include": []string{"refs/heads/main"}, "exclude": []string{}}}
			}
			if !guarded.Load() {
				delete(v, "bypass_actors")
			}
			val = v
		case "/graphql":
			var b struct {
				Query     string
				Variables obj
			}
			json.NewDecoder(r.Body).Decode(&b)
			target := "main"
			if retarget.Load() {
				target = "production"
			}
			pr := obj{"number": 18, "id": "PR_fixture", "isDraft": false, "headRepository": obj{"databaseId": 11}, "headRefOid": head, "baseRefName": target, "baseRef": obj{"target": obj{"oid": base}}, "mergeQueue": obj{"id": "Q"}, "title": "Feature", "state": "OPEN", "reviewDecision": "APPROVED", "commits": obj{"nodes": []obj{{"commit": obj{"oid": head, "statusCheckRollup": obj{"state": "SUCCESS", "contexts": obj{"totalCount": 1}}}}}}}
			if enqueued.Load() {
				pr["mergeQueueEntry"] = obj{"id": "entry", "state": "QUEUED"}
			}
			if strings.Contains(b.Query, "mutation BugsnitchEnqueue") {
				in, ok := b.Variables["input"].(map[string]any)
				if !ok || in["expectedHeadOid"] != head || in["pullRequestId"] != "PR_fixture" {
					t.Error("enqueue subject", in)
				}
				enqueues.Add(1)
				enqueued.Store(true)
				return nil, errors.New("accepted and lost response")
			}
			val = obj{"data": obj{"repository": obj{"databaseId": 11, "nameWithOwner": "example/test", "owner": obj{"__typename": "Organization"}, "pullRequest": pr, "issue": obj{"number": 17, "id": "I17", "state": "OPEN", "title": "Ticket", "body": "requirements"}}}}
		case "/repos/example/test/commits/" + head + "/check-runs", "/repos/example/test/commits/" + group + "/check-runs":
			checkList := []obj{}
			for sha, v := range checks {
				if strings.Contains(r.URL.Path, sha) {
					checkList = append(checkList, v)
				}
			}
			val = obj{"check_runs": checkList}
		case "/repos/example/test/check-runs":
			var b obj
			json.NewDecoder(r.Body).Decode(&b)
			sha := b["head_sha"].(string)
			if sha == head {
				headChecks.Add(1)
			} else if sha == group {
				groupChecks.Add(1)
			} else {
				t.Error("unexpected check target", sha)
			}
			b["app"] = obj{"id": 9}
			checks[sha] = b
			if sha == group && groupChecks.Load() == 1 {
				return nil, errors.New("lost group check acknowledgement")
			}
			val = b
			status = 201
		case "/repos/example/test/git/ref/heads/" + groupRef:
			val = obj{"ref": "refs/heads/" + groupRef, "object": obj{"sha": group, "type": "commit"}}
		case "/repos/example/test/git/commits/" + group:
			parents := []obj{{"sha": base}, {"sha": head}}
			if multiple.Load() {
				parents[1]["sha"] = strings.Repeat("e", 40)
			}
			val = obj{"sha": group, "parents": parents}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			status = 500
		}
		data, _ := json.Marshal(val)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})
	app, err := github.NewLoopbackFixture("fixture", file, origin)
	if err != nil {
		t.Fatal(err)
	}
	s := Store{DB: db, App: app}
	p, err := s.Refresh(ctx, "personal", "p", 18, 17, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"combined_validation", "independent_review"} {
		if err = s.Record(ctx, "personal", "p", 18, Evidence{p.Version, kind, strings.Repeat("1", 64)}); err != nil {
			t.Fatal(err)
		}
	}
	sign := SignOff{p.Version, head, base, "main"}
	if err = s.Approve(ctx, "personal", "p", 18, sign); err != nil {
		t.Fatal(err)
	}
	req := QueueRequest{sign, "observe", "staging"}
	decision, err := s.Queue(ctx, "personal", "p", 18, req)
	if err != nil || decision.State != "hold" {
		t.Fatal("unreadable bypass rights", decision, err)
	}
	guarded.Store(true)
	decision, err = s.Queue(ctx, "personal", "p", 18, req)
	if err != nil || decision.State != "eligible" || headChecks.Load() != 0 {
		t.Fatal("observe", decision, err)
	}
	req.Mode = "enforce"
	decision, err = s.Queue(ctx, "personal", "p", 18, req)
	if err != nil || decision.State != "requested" {
		t.Fatal("enforce", decision, err)
	}
	decision, err = s.Queue(ctx, "personal", "p", 18, req)
	if err != nil || decision.State != "requested" {
		t.Fatal("idempotent permit", decision, err)
	}
	saved := p
	advance := func() error { return s.advanceQueue(ctx, "personal", "p", 18, p) }
	// Revoking dispatch before consumption never publishes a check.
	sqlRun("UPDATE projects SET paused=true")
	if err = advance(); err != nil || headChecks.Load() != 0 {
		t.Fatal("paused", err)
	}
	state := func() string {
		var state string
		if err := db.QueryRow("SELECT state FROM pull_queue_permits").Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	if state() != "held" {
		t.Fatal("paused permit still active")
	}
	sqlRun("UPDATE projects SET paused=false;UPDATE pull_queue_permits SET state='requested'")
	retarget.Store(true)
	if err = advance(); err == nil && state() != "held" {
		t.Fatal("retargeted subject accepted")
	}
	if headChecks.Load() != 0 {
		t.Fatal("posted check on retargeted head")
	}
	retarget.Store(false)
	sqlRun("UPDATE pull_queue_permits SET state='requested'")
	if err = advance(); err == nil || state() != "submitting" || enqueues.Load() != 1 {
		t.Fatal("uncertain enqueue", state(), err)
	}
	p, err = s.Refresh(ctx, "personal", "p", 18, 17, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err = advance(); err != nil || state() != "queued" || enqueues.Load() != 1 {
		t.Fatal("enqueue recovery", state(), err)
	}
	if err = advance(); err != nil || groupChecks.Load() != 0 {
		t.Fatal("unsigned group accepted", err)
	}
	sqlRun(`INSERT INTO github_merge_groups VALUES('personal','p',10,11,'fixture-delivery',$1,$2,$3,'main',now())`, group, base, groupRef)
	multiple.Store(true)
	if err = advance(); err != nil || groupChecks.Load() != 0 {
		t.Fatal("incompatible group accepted", err)
	}
	multiple.Store(false)
	if err = advance(); err == nil || groupChecks.Load() != 1 || state() != "queued" {
		t.Fatal("uncertain group check", err)
	}
	if err = advance(); err != nil || state() != "passed" || groupChecks.Load() != 1 || headChecks.Load() != 1 {
		t.Fatal("verified group", state(), groupChecks.Load(), err)
	}
	if err = advance(); err != nil || groupChecks.Load() != 1 || enqueues.Load() != 1 {
		t.Fatal("duplicate side effect", err)
	}
	// A shared repository can isolate enforcement to staging. Bugsnitch
	// must not call the retargetable enqueue mutation in this topology.
	localOnly.Store(true)
	enqueued.Store(false)
	p, err = s.Refresh(ctx, "personal", "p", 18, 17, p.Version)
	if err != nil {
		t.Fatal(err)
	}
	sign.Version = p.Version
	for _, kind := range []string{"combined_validation", "independent_review"} {
		if err = s.Record(ctx, "personal", "p", 18, Evidence{p.Version, kind, strings.Repeat("2", 64)}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Approve(ctx, "personal", "p", 18, sign); err != nil {
		t.Fatal(err)
	}
	req.SignOff = sign
	req.Mode = "enforce"
	if _, err = s.Queue(ctx, "personal", "p", 18, req); err != nil {
		t.Fatal(err)
	}
	if err = advance(); err != nil {
		t.Fatal(err)
	}
	var latest string
	if err = db.QueryRow("SELECT state FROM pull_queue_permits ORDER BY version DESC LIMIT 1").Scan(&latest); err != nil || latest != "awaiting_queue" || enqueues.Load() != 1 {
		t.Fatal("unexpected auto-enqueue", latest, enqueues.Load(), err)
	}
	req.Mode = "revoke"
	if _, err = s.Queue(ctx, "personal", "p", 18, req); err != nil {
		t.Fatal(err)
	}
	enqueued.Store(true)
	if err = advance(); err != nil || groupChecks.Load() != 1 {
		t.Fatal("group check after revocation", err)
	}

	req.Mode = "renew"
	decision, err = s.Queue(ctx, "personal", "p", 18, req)
	if err != nil || decision.State != "review_required" {
		t.Fatal("fresh review", decision, err)
	}
	pulls, err := s.List(ctx, "personal")
	if err != nil || len(pulls) != 1 || pulls[0].SignOff != nil || pulls[0].Version <= req.Version {
		t.Fatal("stale sign-off survived renew", err)
	}

}
