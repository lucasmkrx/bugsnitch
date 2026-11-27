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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/device"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

type unattendedWaitWriter struct {
	bytes.Buffer
	onWait      func()
	onPlan      func(protocol.NextAction)
	onCandidate func()
}

func (w *unattendedWaitWriter) Write(p []byte) (int, error) {
	var action protocol.NextAction
	if w.onPlan != nil && json.Unmarshal(p, &action) == nil && action.Kind == "review_plan" && action.Answer == "" && action.ExpiresAt != nil {
		w.onPlan(action)
	}
	if w.onCandidate != nil && json.Unmarshal(p, &action) == nil && action.Kind == "approve_draft" {
		w.onCandidate()
	}
	if bytes.Contains(p, []byte("Waiting for prerequisite integration evidence")) {
		w.onWait()
	}
	return w.Buffer.Write(p)
}

func TestUnattendedBatchRestartsAndStartsOnlyAuthorizedIssues(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	const repo = "fixture/unattended"
	const stamp = "2026-11-21T10:00:00Z"
	project := owner.post(t, "projects", object{"name": "Unattended batch", "repository": repo}, 201)["id"].(string)
	db := func(query string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	db("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal',$1,30,31)", project)
	plan := domain.Plan{Title: "Correct display", Milestone: "Validated candidates"}
	sources := map[int]github.Issue{}
	for n := 1; n <= 4; n++ {
		i := github.Issue{ID: int64(40 + n), Number: int64(n), Title: fmt.Sprintf("Issue %d", n), Body: "Fixture requirements", State: "open", UpdatedAt: stamp}
		sources[n] = i
		ticket := domain.Ticket{Number: n, Title: i.Title, Criteria: []string{"Show fixed label"}, Verification: "Check label", EstimateHours: 1}
		if n == 2 {
			ticket.Dependencies = []int{1}
		}
		plan.Tickets = append(plan.Tickets, ticket)
		db(`INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,checked_at,selection_revision) VALUES('personal',$1,$2,$3,30,31,$4,$5,'open','current',now(),$6)`, project, n, i.ID, i.Title, i.RequirementsDigest(), stamp)
	}
	planID := owner.post(t, "plans", object{"projectId": project, "version": 1, "plan": plan}, 201)["id"].(string)
	owner.post(t, "plans/"+planID+"/approve", object{"version": 2}, 200)
	code := owner.post(t, "pairings", object{"name": "Batch runner", "projectIds": []string{project}}, 201)["code"].(string)
	var turns atomic.Int32
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = routedTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "localhost:8787":
			rec := httptest.NewRecorder()
			// The in-process transport waits for the proxy. A real HTTP client
			// can abandon a long poll without treating that as a server error.
			if r.URL.Path == "/api/v1/device/progression-wake" {
				r = r.Clone(context.WithoutCancel(r.Context()))
			}
			owner.handler.ServeHTTP(rec, r)
			if rec.Code >= 400 {
				t.Logf("%s returned %d: %s", r.URL.Path, rec.Code, rec.Body.String())
			}
			return rec.Result(), nil
		case "api.github.com":
			n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/"+repo+"/issues/"))
			i, ok := sources[n]
			if err != nil || !ok || n == 1 || n == 4 || r.Method != http.MethodGet {
				return nil, fmt.Errorf("unexpected GitHub operation: %s %s", r.Method, r.URL.Path)
			}
			return unattendedResponse(object{"id": i.ID, "number": i.Number, "title": i.Title, "body": i.Body, "state": i.State, "updated_at": stamp, "html_url": fmt.Sprintf("https://github.com/%s/issues/%d", repo, n)})
		case "api.anthropic.com":
			n := turns.Add(1)
			answers := []string{
				`{"summary":"Correct the label and check the output","disposition":"ready","question":""}`,
				`{"summary":"Corrected label","edits":[{"path":"label.txt","content":"fixed\n"}]}`,
				`{"summary":"Verified the correction","findings":[]}`,
			}
			return unattendedResponse(object{"id": fmt.Sprintf("turn-%d", n), "model": "fixture-claude", "stop_reason": "end_turn", "content": []object{{"type": "text", "text": answers[(n-1)%3]}}})
		default:
			return nil, errors.New("unexpected outbound host")
		}
	})

	setupPairedWorker(t, s, repo, project, code)

	arm := []string{"arm", "--project", project, "--issues", "2,3", "--for", "5m", "--turns", "6", "--plan-approver", "owner"}
	var preview bytes.Buffer
	if err := device.LocalUnattended(t.Context(), arm, &preview); err != nil {
		t.Fatal(err)
	}
	var authorization struct {
		Digest string `json:"approvalDigest"`
	}
	if err := json.Unmarshal(preview.Bytes(), &authorization); err != nil || authorization.Digest == "" {
		t.Fatal(preview.String(), err)
	}
	if err := device.LocalUnattended(t.Context(), append(arm, "--approve", authorization.Digest), io.Discard); err != nil {
		t.Fatal(err)
	}
	run := []string{"run", "--project", project}
	first, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	var approvals int
	approvePlan := func(action protocol.NextAction) {
		path := "attempts/" + action.AttemptID + "/ticket-decision"
		owner.post(t, path, object{"version": action.Version + 1, "answer": "approve"}, 409)
		owner.post(t, path, object{"version": action.Version, "answer": "approve"}, 200)
		owner.post(t, path, object{"version": action.Version, "answer": "approve"}, 200)
		owner.post(t, path, object{"version": action.Version, "answer": "hold"}, 409)
		approvals++
	}
	log := unattendedWaitWriter{onWait: func() {}, onPlan: approvePlan, onCandidate: cancel}
	if err := device.LocalUnattended(first, run, &log); !errors.Is(err, context.Canceled) || turns.Load() != 3 {
		t.Fatal(log.String(), turns.Load(), err)
	}
	second, end := context.WithTimeout(t.Context(), 25*time.Second)
	defer end()
	restartedLog := unattendedWaitWriter{onPlan: approvePlan, onWait: func() {
		owner.post(t, "plans/"+planID+"/evidence", object{"number": 1, "revision": strings.Repeat("a", 40), "summary": "Verified integration"}, 201)
	}}
	if err := device.LocalUnattended(second, run, &restartedLog); err != nil {
		t.Fatal(restartedLog.String(), err)
	}
	list, err := runner.Summaries()
	if err != nil || len(list) != 2 || turns.Load() != 6 || approvals != 2 {
		t.Fatal(list, turns.Load(), err)
	}
	for _, a := range list {
		if (a.IssueNumber != 2 && a.IssueNumber != 3) || a.State != "completed" {
			t.Fatal(a)
		}
	}
	if err := device.LocalUnattended(second, run, io.Discard); err == nil || turns.Load() != 6 {
		t.Fatal("closed grant restarted", err)
	}

	arm = []string{"arm", "--project", project, "--issues", "4", "--for", "5m", "--turns", "3"}
	preview.Reset()
	if err := device.LocalUnattended(t.Context(), arm, &preview); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(preview.Bytes(), &authorization); err != nil {
		t.Fatal(err)
	}
	if err := device.LocalUnattended(t.Context(), append(arm, "--approve", authorization.Digest), io.Discard); err != nil {
		t.Fatal(err)
	}
	db("UPDATE planned_issue_state SET selection_revision='2026-11-21T11:00:00Z' WHERE number=4")
	var held bytes.Buffer
	if err := device.LocalUnattended(t.Context(), run, &held); err != nil || !strings.Contains(held.String(), "no longer matches the authorized revision") || turns.Load() != 6 {
		t.Fatal(held.String(), turns.Load(), err)
	}
}

func unattendedResponse(value any) (*http.Response, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func setupPairedWorker(t *testing.T, s *testServer, repo, project, code string) string {
	t.Helper()
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	t.Setenv("BUGSNITCH_ANTHROPIC_API_KEY", "fixture-api-key")
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "fixture-read-only")
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=test@fixture.invalid"}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(args, string(output), err)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(dir, "label.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "label.txt")
	git("commit", "-m", "baseline")
	git("remote", "add", "origin", "https://github.com/"+repo+".git")
	if err := device.RunCLI([]string{"pair", "--server", s.Origin, "--code", code}); err != nil {
		t.Fatal(err)
	}
	if err := device.RunCLI([]string{"register", "--project", project, "--path", dir}); err != nil {
		t.Fatal(err)
	}
	stage := runner.StageSettings{Provider: "claude", Model: "fixture-claude"}
	if err := runner.AddProject("personal", repo, dir, runner.Config{RequirePlanReview: true, Files: []string{"label.txt"}, Validate: []string{"/usr/bin/grep", "-Fxq", "fixed", "label.txt"}, Stages: map[string]runner.StageSettings{"plan": stage, "implement": stage, "review": stage}}); err != nil {
		t.Fatal(err)
	}
	start, finish, tstart, tend, lease := runner.BeforeStart, runner.AfterFinish, runner.BeforeTurn, runner.AfterTurn, runner.CheckWorker
	t.Cleanup(func() {
		runner.BeforeStart, runner.AfterFinish, runner.BeforeTurn, runner.AfterTurn, runner.CheckWorker = start, finish, tstart, tend, lease
	})
	runner.BeforeStart, runner.AfterFinish, runner.BeforeTurn, runner.AfterTurn, runner.CheckWorker = device.Admit, device.Finish, device.TurnStart, device.TurnEnd, device.CheckWorker

	return dir
}
