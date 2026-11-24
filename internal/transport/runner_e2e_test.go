// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/device"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
	"github.com/lucasmkrx/bugsnitch/internal/testcodex"
)

func TestMain(m *testing.M) { testcodex.Main(m) }

type routedTransport func(*http.Request) (*http.Response, error)

func (f routedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPairedWorkerAndDashboardEvidence(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	ctx := context.Background()
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	repo := "fixture/paired"
	criteria := []string{"The label is fixed."}
	id := owner.post(t, "projects", object{"name": "Paired worker fixture", "repository": repo}, 201)["id"].(string)
	plan := domain.Plan{Title: "Restore label", Milestone: "Display", Tickets: []domain.Ticket{{Number: 7, Title: "Fix the label", Criteria: criteria, Verification: "Check label.txt", EstimateHours: 1}}}
	proposal := owner.post(t, "plans", object{"projectId": id, "version": 1, "plan": plan}, 201)["id"].(string)
	owner.post(t, "plans/"+proposal+"/approve", object{"version": 2}, 200)
	code := owner.post(t, "pairings", object{"name": "Fixture device", "projectIds": []string{id}}, 201)["code"].(string)
	issue := runner.Issue{Schema: 1, Repository: repo, Number: 7, URL: "https://github.com/" + repo + "/issues/7", Title: "Fix label", State: "open", UpdatedAt: "2026-11-21T08:00:00Z", Criteria: criteria}
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	http.DefaultTransport = routedTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" && r.URL.Path == "/repos/"+repo+"/issues/7" {
			b, _ := json.Marshal(object{"state": "open", "title": issue.Title, "number": 7, "html_url": issue.URL, "updated_at": issue.UpdatedAt})
			return &http.Response{Request: r, StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(b))}, nil
		}
		if r.URL.Host != "localhost:8787" {
			return nil, fmt.Errorf("unexpected test origin")
		}
		record := httptest.NewRecorder()
		owner.handler.ServeHTTP(record, r)
		return record.Result(), nil
	})
	cli := func(args ...string) {
		t.Helper()
		if err := device.RunCLI(args); err != nil {
			t.Fatal(args[0], err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=f@fixture.invalid", "-C", dir}, args...)...)
		if err := cmd.Run(); err != nil {
			t.Fatal(args[0], err)
		}
	}
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(checkout, "init")
	if err = os.WriteFile(filepath.Join(checkout, "label.txt"), []byte("wrong\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(checkout, "remote", "add", "origin", "https://github.com/"+repo+".git")
	git(checkout, "add", "--", "label.txt")
	git(checkout, "commit", "-m", "baseline")
	cli("pair", "--server", s.Origin, "--code", code)
	cli("register", "--project", id, "--path", checkout)
	if err = runner.AddProject("personal", repo, checkout, runner.Config{Files: []string{"label.txt"}, Validate: []string{"/usr/bin/grep", "-Fx", "fixed", "label.txt"}}); err != nil {
		t.Fatal(err)
	}
	testcodex.Install(t)
	priorWorkerMeter, priorGeneralMeter := runner.MeasureWorker, runner.MeasureGeneral
	runner.MeasureWorker, runner.MeasureGeneral = device.MeasureWorker, device.MeasureGeneral
	defer func() { runner.MeasureWorker, runner.MeasureGeneral = priorWorkerMeter, priorGeneralMeter }()
	previousCheck := runner.CheckWorker
	runner.CheckWorker = device.CheckWorker
	defer func() { runner.CheckWorker = previousCheck }()
	b, f, bt, et := runner.BeforeStart, runner.AfterFinish, runner.BeforeTurn, runner.AfterTurn
	defer func() { runner.BeforeStart, runner.AfterFinish, runner.BeforeTurn, runner.AfterTurn = b, f, bt, et }()
	runner.BeforeStart, runner.AfterFinish, runner.BeforeTurn, runner.AfterTurn = device.Admit, device.Finish, device.TurnStart, device.TurnEnd
	a, err := runner.Start(ctx, "personal", repo, issue)
	if err != nil || a.State != "waiting" {
		t.Fatal("plan", a.Summary, err)
	}
	cli("sync")
	owner.post(t, "attempts/"+a.ID+"/control", object{"operation": "pause", "requestId": "pause_fixture_request", "version": a.Version}, 202)
	cli("sync")
	cli("sync")
	a, err = runner.Continue(ctx, a.ID, "Apply the planned correction.")
	if err != nil || a.State != "completed" {
		t.Fatal("continue", a.Summary, err)
	}
	cli("sync")
	cli("sync")
	oldBinding, oldSubmit := runner.BindGeneral, runner.SubmitGeneral
	defer func() { runner.BindGeneral, runner.SubmitGeneral = oldBinding, oldSubmit }()
	runner.BindGeneral, runner.SubmitGeneral = device.BindGeneral, device.SubmitGeneral
	proposalResult, err := runner.DraftPlan(ctx, "personal", "Bounded test-only brief", "", "")
	if err != nil || len(proposalResult.Tickets) != 1 {
		t.Fatal("paired planner", err)
	}
	reply, err := runner.Manager(ctx, "personal", repo, random(16), "Private fixture manager question", "", "")
	if err != nil || reply.Status != "completed" {
		t.Fatal("paired manager", err)
	}
	cli("sync")
	usage := owner.post(t, "usage", object{}, 200)
	modelRows, ok := usage["models"].([]any)
	if !ok || len(modelRows) != 1 || usage["admittedTurns"] != float64(5) || usage["unmeteredTurns"] != float64(0) || modelRows[0].(map[string]any)["inputTokens"] != float64(205) {
		t.Fatalf("worker, planner and manager metering: %#v", usage)
	}
	var ended int
	if err = s.DB.QueryRow("SELECT count(*) FROM general_model_turns WHERE version=2").Scan(&ended); err != nil || ended != 2 {
		t.Fatal("general permits were not released", ended, err)
	}
	var held, processing bool
	var seq int
	if err = s.DB.QueryRow("SELECT active,in_turn,sequence FROM admission WHERE attempt_id=$1", a.ID).Scan(&held, &processing, &seq); err != nil || held || processing || seq != 10 {
		t.Fatal("admission lifecycle", held, processing, seq, err)
	}
	_, state := owner.req(t, "GET", "/api/v1/state", nil, "")
	body, _ := json.Marshal(state)
	if bytes.Contains(body, []byte("Private fixture manager question")) || bytes.Contains(body, []byte("Fixture manager reply")) {
		t.Fatal("local conversation reached hosted state")
	}
	output, _ := json.Marshal(state["attempts"])
	for _, v := range []string{a.ID, a.CandidateSHA, `"exitCode":0`, `"state":"completed"`} {
		if !bytes.Contains(output, []byte(v)) {
			t.Fatalf("missing dashboard evidence %s", v)
		}
	}
	for _, v := range []string{checkout, "label.txt", "Fixture review checked"} {
		if bytes.Contains(output, []byte(v)) {
			t.Fatal("private stage text reached browser")
		}
	}
}
