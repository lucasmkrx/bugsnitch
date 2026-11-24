// SPDX-License-Identifier: MPL-2.0
package incidents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/testgithub"
)

// CheckoutFixture connects production handlers and outbox processing to two
// in-process provider fixtures. The external test uses the real runner package.
type CheckoutFixture struct {
	t      *testing.T
	s      fixtureStore
	issue  protocol.IssueSelection
	body   string
	writes int
	origin string
	mu     sync.Mutex
}

func NewCheckoutFixture(t *testing.T) *CheckoutFixture {
	f := &CheckoutFixture{t: t, s: disposableStore(t)}
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = transportFixture(f.github)
	f.origin = testgithub.Serve(t, f.github)
	return f
}

func (f *CheckoutFixture) github(r *http.Request) (*http.Response, error) {
	const repo = "example/disposable"
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Host != "" && r.URL.Host != "api.github.com" {
		return nil, errors.New("unexpected fixture host")
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /app":
		return jsonResponse(200, map[string]any{"id": 50, "slug": "checkout-fixture"}), nil
	case "POST /app/installations/88/access_tokens":
		return jsonResponse(201, map[string]string{"token": "fixture-installation-token"}), nil
	case "GET /repositories/99":
		return jsonResponse(200, map[string]any{"id": 99, "full_name": repo}), nil
	case "POST /repos/" + repo + "/issues":
		var draft struct{ Title, Body string }
		if err := json.NewDecoder(r.Body).Decode(&draft); err != nil {
			return nil, err
		}
		if !strings.Contains(draft.Body, "ShippingThresholdError") {
			return nil, errors.New("incident evidence missing from promoted issue")
		}
		f.writes++
		f.body = draft.Body
		f.issue = protocol.IssueSelection{Schema: 1, Repository: repo, Number: 7, State: "open", URL: "https://github.com/" + repo + "/issues/7", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Title: draft.Title}
		return jsonResponse(201, f.issueRecord()), nil
	case "GET /repos/" + repo + "/issues/7":
		return jsonResponse(200, f.issueRecord()), nil
	}
	return nil, errors.New("unexpected GitHub fixture operation")
}

func (f *CheckoutFixture) issueRecord() map[string]any {
	i := f.issue
	return map[string]any{"id": 55, "number": i.Number, "title": i.Title, "body": f.body, "state": i.State, "html_url": i.URL, "updated_at": i.UpdatedAt}
}

func (f *CheckoutFixture) Deliver(projected map[string]any, replay bool, expectedOccurrences int64) {
	t := f.t
	t.Helper()
	e := make(map[string]any, len(projected)+3)
	for k, v := range projected {
		e[k] = v
	}
	occurred, ok := projected["timestamp"].(float64)
	if !ok || occurred <= 0 {
		t.Fatal("the captured browser envelope needs an occurrence timestamp")
	}
	e["project"], e["issue_id"], e["datetime"] = 21, "42", time.Unix(0, int64(occurred*1e9)).UTC()
	payload, err := json.Marshal(map[string]any{"action": "created", "installation": map[string]string{"uuid": installation}, "data": map[string]any{"error": e}})
	if err != nil {
		t.Fatal(err)
	}
	send := func() {
		t.Helper()
		if status := signedEvent(f.s, payload); status != 202 {
			t.Fatalf("signed error.created returned %d", status)
		}
	}
	send()
	if replay {
		send()
	}
	if err = f.s.processAccepted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := one(t, f.s); got.Occurrences != expectedOccurrences || got.Observed != "ShippingThresholdError reported by Sentry" {
		t.Fatalf("wrong incident grouping: %s, %d occurrences", got.Observed, got.Occurrences)
	}
}

func (f *CheckoutFixture) Promote() protocol.IssueSelection {
	t := f.t
	t.Helper()
	ctx := context.Background()
	inc := one(t, f.s)
	for range 2 {
		if err := f.s.Promote(ctx, "personal", "p", inc.ID, inc.Version); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.syncOne(ctx, fixtureApp(t, f.origin)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	inc = one(t, f.s)
	if inc.IssueNumber == nil || *inc.IssueNumber != int64(f.issue.Number) || inc.Promotion == nil || *inc.Promotion != "linked" || f.writes != 1 {
		t.Fatal("promotion did not produce one linked canonical ticket")
	}
	if err := f.s.DB.QueryRowContext(ctx, "SELECT issue_id FROM incident_issues WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3", "personal", "p", inc.ID).Scan(&f.issue.GitHubID); err != nil {
		t.Fatal(err)
	}
	f.issue.RequirementsDigest = (github.Issue{Title: f.issue.Title, Body: f.body}).RequirementsDigest()
	t.Logf("Incident %s: %d occurrences, one promoted issue %s (GitHub App fixture)", inc.ID, inc.Occurrences, f.issue.URL)
	return f.issue
}

func (f *CheckoutFixture) Observe(r Receipt, want string) {
	t := f.t
	t.Helper()
	inc := one(t, f.s)
	if r.Kind != "" {
		r.At, r.Version = time.Now().UTC(), inc.Version
		if r.CommandID == "" {
			r.CommandID = "checkout-" + r.Kind
		}
		if err := f.s.RecordReceipt(context.Background(), "personal", "p", inc.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	v, err := f.s.Verdict(context.Background(), "personal", "p", inc.ID, "bugsnitch-fixture")
	if err != nil || v.Status != want {
		t.Fatalf("verdict=%s want=%s err=%v", v.Status, want, err)
	}
	t.Logf("Incident %s: %s", inc.ID, v.Status)
}

type checkoutMonitor struct {
	MonitorRegistration
	candidate string
}

func (f *CheckoutFixture) Monitor(candidate string) checkoutMonitor {
	f.t.Helper()
	inc := one(f.t, f.s)
	reg, err := f.s.ConfigureMonitor(f.t.Context(), "personal", "p", inc.ID, MonitorConfig{
		Version: inc.Version, Candidate: candidate, Environment: "bugsnitch-fixture", Check: "Chromium shipping threshold",
		Deployer: 10, Verifier: 20,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return checkoutMonitor{reg, candidate}
}

func (f *CheckoutFixture) sendRelease(event string, delivery int, payload map[string]any) {
	f.t.Helper()
	payload["installation"] = map[string]int{"id": 88}
	payload["repository"] = map[string]int{"id": 99}
	b, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	resp := post(f.s.listener, "/hooks/github", b, map[string]string{
		"X-GitHub-Delivery": fmt.Sprintf("00000000-0000-4567-89ab-%012d", delivery),
		"X-GitHub-Event":    event, "X-Hub-Signature-256": "sha256=" + sign("github-fixture", b),
	})
	if resp.Code != 202 {
		f.t.Fatalf("%s HTTP %d: %s", event, resp.Code, resp.Body.String())
	}
}

func (f *CheckoutFixture) Deployed(m checkoutMonitor, deployment int, build string) {
	f.t.Helper()
	type obj = map[string]any
	f.sendRelease("deployment_status", deployment*2, obj{"action": "created",
		"deployment":        obj{"id": deployment, "sha": m.candidate, "creator": obj{"id": 10}, "environment": "bugsnitch-fixture", "payload": obj{"bugsnitchBuild": build}},
		"deployment_status": obj{"id": deployment + 100, "creator": obj{"id": 10}, "state": "success", "environment": "bugsnitch-fixture", "created_at": time.Now().UTC()},
	})
	f.Observe(Receipt{}, "Verification inconclusive")
	f.CheckGate(m, deployment, build, "Verification inconclusive", false)
}

func (f *CheckoutFixture) Verified(m checkoutMonitor, deployment int, build string, started time.Time) {
	f.t.Helper()
	type obj = map[string]any
	correlation, err := json.Marshal(obj{"monitor": m.ID, "deployment": deployment, "status": deployment + 100, "build": build, "exposures": 1, "coverage": true})
	if err != nil {
		f.t.Fatal(err)
	}
	f.sendRelease("check_run", deployment*2+1, obj{"action": "completed", "check_run": obj{"id": deployment + 200, "app": obj{"id": 20}, "head_sha": m.candidate,
		"name": "Chromium shipping threshold", "conclusion": "success", "started_at": started, "completed_at": time.Now().UTC(), "external_id": "bugsnitch/v1 " + string(correlation)}})
	f.Observe(Receipt{}, "Verified in environment")
	f.CheckGate(m, deployment, build, "Verified in environment", true)
}

func (f *CheckoutFixture) CheckGate(m checkoutMonitor, deployment int, build, reason string, allowed bool) {
	f.t.Helper()
	b, err := json.Marshal(map[string]any{"candidate": m.candidate, "build": build, "deployment": deployment, "status": deployment + 100})
	if err != nil {
		f.t.Fatal(err)
	}
	resp := post(f.s.listener, "/api/v1/deployment-gates/"+m.ID, b, map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + m.Token})
	var result GateResult
	want := http.StatusConflict
	if allowed {
		want = http.StatusOK
	}
	if resp.Code != want || json.Unmarshal(resp.Body.Bytes(), &result) != nil || result.Allowed != allowed || result.Reason != reason {
		f.t.Fatalf("gate HTTP %d, %s", resp.Code, resp.Body.String())
	}
}
