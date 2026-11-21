// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestLocalPassPersistsQuestionAndStopsForReview(t *testing.T) {
	_, i := fixture(t)
	i.GitHubID = 77
	i.RequirementsDigest = strings.Repeat("a", 64)
	scope := protocol.RunScope{Device: "original", Project: "project", Plan: "plan", Policy: 3}
	before, end, lease := BeforeTurn, AfterTurn, CheckWorker
	t.Cleanup(func() { BeforeTurn = before; AfterTurn = end; CheckWorker = lease })
	ok := func(s Summary) error {
		if s.Scope == nil || *s.Scope != scope {
			return errors.New("lost pinned scope")
		}
		return nil
	}
	BeforeStart, AfterFinish, BeforeTurn, AfterTurn = ok, ok, ok, ok
	CheckWorker = func(_ context.Context, s Summary, _ bool) (time.Duration, error) { return time.Minute, ok(s) }
	var runs []string
	modelTurn = func(_ context.Context, a *Attempt, _, payload string, _ any) (agent.Result, error) {
		runs = append(runs, a.Phase)
		var value string
		switch a.Phase {
		case "plan":
			value = `{"summary":"Check the selected display preference.","disposition":"needs_owner","question":"Which label should the UI show?"}`
		case "implement":
			if !strings.Contains(payload, "Use fixed.") {
				t.Fatal("owner feedback missing")
			}
			value = `{"summary":"Update the label","edits":[{"path":"example.txt","content":"fixed\n"}]}`
		case "review":
			value = `{"summary":"Further validation required","findings":[{"severity":"important","location":"example.txt","rationale":"Missing evidence for another input"}]}`
		}
		return agent.Result{Text: value}, nil
	}
	started := 0
	a, err := StartPass(context.Background(), "personal", i, scope, func(Summary) { started++ })
	if err != nil || a.State != "waiting" || a.OwnerQuestion == "" || a.CandidateSHA != "" || len(runs) != 1 || started != 1 {
		t.Fatal(a.Summary, err)
	}
	prior := a
	if _, err = Continue(context.Background(), a.ID, " "); err == nil || len(runs) != 1 {
		t.Fatal("advanced without owner answer")
	}
	a, err = Continue(context.Background(), a.ID, "Use fixed.")
	if err != nil || a.State != "waiting" || a.Phase != "implement" || a.Rounds != 1 || a.OwnerAnswer != "Use fixed." || len(runs) != 3 {
		t.Fatal(a.Summary, err, runs)
	}
	duplicate, err := StartPass(context.Background(), "personal", i, scope, func(Summary) { started++ })
	if err == nil || duplicate.ID != prior.ID || started != 1 || len(runs) != 3 {
		t.Fatal("batch resumed an old attempt", err)
	}
}

func TestSensitiveEditsRequireOwnerFeedback(t *testing.T) {
	for _, path := range []string{"service/auth/guard.go", "lib/auth.go", "db/migrations/005.sql", ".github/workflows/release.yml", "deploy/main.tf", ".env.local"} {
		if !protectedChange(path) {
			t.Fatal(path)
		}
	}
	if protectedChange("docs/author.md") {
		t.Fatal("ordinary file classified as protected")
	}
}

func TestConcurrentPassesKeepDistinctWorktreesAndEvidence(t *testing.T) {
	_, first := fixture(t)
	first.GitHubID = 80
	first.RequirementsDigest = strings.Repeat("b", 64)
	second := first
	second.Number++
	second.GitHubID++
	second.URL = "https://github.com/fixture/selected/issues/8"
	before, end, lease := BeforeTurn, AfterTurn, CheckWorker
	t.Cleanup(func() { BeforeTurn = before; AfterTurn = end; CheckWorker = lease })
	allow := func(Summary) error { return nil }
	BeforeStart, AfterFinish, BeforeTurn, AfterTurn = allow, allow, allow, allow
	CheckWorker = func(context.Context, Summary, bool) (time.Duration, error) { return time.Minute, nil }
	scope := protocol.RunScope{Device: "fixture", Project: "selected", Plan: "approved", Policy: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	running := make(chan string, 2)
	proceed := make(chan struct{})
	modelTurn = func(ctx context.Context, a *Attempt, _, _ string, _ any) (agent.Result, error) {
		var answer string
		switch a.Phase {
		case "plan":
			running <- a.ID
			select {
			case <-proceed:
			case <-ctx.Done():
				return agent.Result{}, ctx.Err()
			}
			answer = `{"summary":"Update the approved file and run the validator.","disposition":"ready","question":""}`
		case "implement":
			answer = `{"summary":"Fix the label","edits":[{"path":"example.txt","content":"fixed\n"}]}`
		case "review":
			answer = `{"summary":"Candidate and check satisfy the criterion","findings":[]}`
		}
		return agent.Result{Thread: a.ID + "-" + a.Phase, Text: answer}, nil
	}
	type outcome struct {
		a   Attempt
		err error
	}
	done := make(chan outcome, 2)
	for _, issue := range []Issue{first, second} {
		go func() { a, err := StartPass(ctx, "personal", issue, scope, func(Summary) {}); done <- outcome{a, err} }()
	}
	ids := map[string]bool{}
	var early []outcome
	for range 2 {
		select {
		case id := <-running:
			ids[id] = true
		case got := <-done:
			early = append(early, got)
		case <-ctx.Done():
		}
	}
	close(proceed)
	got := early
	for len(got) < 2 {
		got = append(got, <-done)
	}
	if len(ids) != 2 || got[0].err != nil || got[1].err != nil {
		t.Fatal("independent simultaneous plan stages", ids, got[0].err, got[1].err)
	}
	if got[0].a.Worktree == got[1].a.Worktree || got[0].a.Branch == got[1].a.Branch {
		t.Fatal("attempts shared a checkout")
	}
	for _, v := range got {
		a := v.a
		if a.State != "completed" || len(a.Artifacts) != 4 || a.Artifacts[2].ExitCode == nil || *a.Artifacts[2].ExitCode != 0 || a.Artifacts[1].Thread == a.Artifacts[3].Thread {
			t.Fatal(a.Summary)
		}
	}
}
