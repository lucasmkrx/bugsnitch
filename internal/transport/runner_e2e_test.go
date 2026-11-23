// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bufio"
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
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/device"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

// The test executable doubles as a deterministic local app-server peer only
// when launched through this test's private symlink. No production adapter or
// runtime selection flag can select this fixture.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "codex" {
		os.Exit(codexProtocolFixture(os.Args[1:]))
	}
	os.Exit(m.Run())
}
func codexProtocolFixture(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("codex-cli " + agent.Version)
		return 0
	}
	if len(args) == 0 || args[0] != "app-server" {
		return 1
	}
	in := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	tid := fmt.Sprintf("fixture-thread-%d", os.Getpid())
	turn := "fixture-turn"
	answer := "Plan to change the approved text file."
	for in.Scan() {
		var req struct {
			ID     *int
			Method string
			Params struct {
				Cwd, DeveloperInstructions string
				Input                      []struct{ Text string }
			}
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			return 1
		}
		if req.ID == nil {
			continue
		}
		var out any = object{}
		switch req.Method {
		case "initialize":
		case "account/read":
			out = object{"account": object{"type": "chatgpt"}}
		case "mcpServerStatus/list":
			out = object{"data": []any{}}
		case "model/list":
			out = object{"data": []any{object{"id": "fixture", "model": "fixture", "isDefault": true}}}
		case "thread/start":
			if strings.Contains(req.Params.DeveloperInstructions, "Return disposition ready") {
				answer = `{"summary":"Change the approved label and run the specified validation","disposition":"ready","question":""}`
			}
			if strings.Contains(req.Params.DeveloperInstructions, "overall project planner") {
				answer = `{"title":"Fixture draft","milestone":"Ready","assumptions":"Test-only protocol data","target":"","forecast":"","tickets":[{"number":1,"title":"Check label","criteria":["Label visible"],"verification":"Run fixture","estimateHours":1,"priority":1,"dependencies":[],"resources":[]}]}`
			}
			if strings.Contains(req.Params.DeveloperInstructions, "project manager responding") {
				answer = "Fixture manager reply."
			}
			if strings.Contains(req.Params.DeveloperInstructions, "complete UTF-8 replacement") {
				answer = ""
			}
			if strings.Contains(req.Params.DeveloperInstructions, "Independently inspect") {
				answer = `{"summary":"Fixture review checked the candidate","findings":[]}`
			}
			out = object{"thread": object{"id": tid}, "cwd": req.Params.Cwd, "sandbox": object{"type": "readOnly"}, "model": "fixture", "modelProvider": "openai", "approvalPolicy": "never"}
		case "turn/start":
			if answer == "" {
				var task struct {
					Issue runner.Issue
					Files map[string]string
				}
				if len(req.Params.Input) != 1 || json.Unmarshal([]byte(req.Params.Input[0].Text), &task) != nil {
					return 1
				}
				path := "label.txt"
				if task.Issue.Repository == "fixture/batch" {
					path = fmt.Sprintf("label-%d.txt", task.Issue.Number)
				}
				if _, approved := task.Files[path]; !approved {
					return 1
				}
				answer = fmt.Sprintf(`{"summary":"Updated example","edits":[{"path":%q,"content":"fixed\n"}]}`, path)
			}
			out = object{"turn": object{"id": turn}}
		default:
			return 1
		}
		if encoder.Encode(object{"id": *req.ID, "result": out}) != nil {
			return 1
		}
		if req.Method == "turn/start" {
			encoder.Encode(object{"method": "item/completed", "params": object{"threadId": tid, "turnId": turn, "item": object{"type": "agentMessage", "text": answer, "phase": "final_answer"}}})
			encoder.Encode(object{"method": "turn/completed", "params": object{"threadId": tid, "turn": object{"id": turn, "status": "completed"}}})
		}
	}
	return 0
}

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
	fixturePath := t.TempDir()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(testBinary, filepath.Join(fixturePath, "codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fixturePath+string(os.PathListSeparator)+os.Getenv("PATH"))
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
