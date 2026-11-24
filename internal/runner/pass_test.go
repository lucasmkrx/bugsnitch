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
	BeforeStart, AfterFinish, BeforeTurn, AfterTurn = ok, ok, func(_ context.Context, s Summary) error { return ok(s) }, ok
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

func TestQueuedWorkerReleasesPermitAndStopsOnLostAuthority(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	if _, err := SetLocalLimits(0, 1); err != nil {
		t.Fatal(err)
	}
	previousStart, previousCheck := BeforeTurn, CheckWorker
	t.Cleanup(func() { BeforeTurn, CheckWorker = previousStart, previousCheck })
	starts := 0
	BeforeTurn = func(context.Context, Summary) error {
		starts++
		return ErrHostedTurnCapacity
	}
	CheckWorker = func(_ context.Context, _ Summary, inTurn bool) (time.Duration, error) {
		if inTurn {
			t.Fatal("queued worker reported that its turn had started")
		}
		release, err := acquireModel("another worker")
		if err != nil {
			t.Fatalf("queued worker held the only local permit: %v", err)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
		return 0, ErrAuthorityStale
	}
	a := &Attempt{LocalRunScope: &protocol.RunScope{Device: "original"}}
	if _, err := reserveWorkerTurn(t.Context(), a); !errors.Is(err, ErrAuthorityStale) || starts != 1 {
		t.Fatalf("turn reservations after authority changed: starts=%d err=%v", starts, err)
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

func TestReviewedCheckpointAndBoundedAutomaticCorrections(t *testing.T) {
	dir, issue := fixture(t)
	issue.GitHubID = 77
	issue.RequirementsDigest = strings.Repeat("a", 64)
	scope := protocol.RunScope{Device: "fixture", Project: "selected", Plan: "approved", Policy: 2}
	before, after, check := BeforeTurn, AfterTurn, CheckWorker
	t.Cleanup(func() { BeforeTurn, AfterTurn, CheckWorker = before, after, check })
	BeforeStart = func(Summary) error { return nil }
	AfterFinish, AfterTurn = BeforeStart, BeforeStart
	BeforeTurn = func(context.Context, Summary) error { return nil }
	CheckWorker = func(context.Context, Summary, bool) (time.Duration, error) { return time.Minute, nil }
	if err := AddProject("personal", issue.Repository, dir, Config{
		Files: []string{"example.txt"}, Validate: []string{"/bin/sh", "-c", "true"},
		RequirePlanReview: true, AutoCorrect: true,
	}); err != nil {
		t.Fatal(err)
	}
	var stages []string
	modelTurn = func(_ context.Context, a *Attempt, _, _ string, _ any) (agent.Result, error) {
		stages = append(stages, a.Phase)
		switch a.Phase {
		case "plan":
			return agent.Result{Text: `{"summary":"Implement the approved change","disposition":"ready","question":""}`}, nil
		case "implement":
			edits := []string{"first", "second", "fixed"}
			return agent.Result{Text: `{"summary":"Update the value","edits":[{"path":"example.txt","content":"` + edits[a.Rounds] + `\n"}]}`}, nil
		case "review":
			return agent.Result{Text: `{"summary":"A correction remains","findings":[{"severity":"important","location":"example.txt","rationale":"Retain the review stop"}]}`}, nil
		}
		return agent.Result{}, errors.New("unexpected worker stage")
	}
	a, err := StartPass(t.Context(), "personal", issue, scope, func(Summary) {})
	if err != nil || a.State != "waiting" || a.ReasonCode != protocol.PlanReviewRequired || len(stages) != 1 {
		t.Fatal(a.Summary, stages, err)
	}
	version := a.Version
	if _, err := ContinueAt(t.Context(), a.ID, "", version-1); err == nil || len(stages) != 1 {
		t.Fatal("stale approval started a worker", err)
	}
	result, err := ContinueAt(t.Context(), a.ID, "", version)
	if err == nil || result.State != "failed" || result.Rounds != 2 || len(stages) != 7 {
		t.Fatal(result.Summary, stages, err)
	}
	if _, err := ContinueAt(t.Context(), a.ID, "", version); err == nil || len(stages) != 7 {
		t.Fatal("replayed approval started a worker", err)
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
	BeforeStart, AfterFinish, BeforeTurn, AfterTurn = allow, allow, func(_ context.Context, s Summary) error { return allow(s) }, allow
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

func TestBatchCapacityLossLeavesIssueSelectable(t *testing.T) {
	_, issue := fixture(t)
	if _, err := SetLocalLimits(1, 1); err != nil {
		t.Fatal(err)
	}
	previousStart, previousEnd, previousLease := BeforeTurn, AfterTurn, CheckWorker
	t.Cleanup(func() { BeforeTurn, AfterTurn, CheckWorker = previousStart, previousEnd, previousLease })
	allow := func(Summary) error { return nil }
	BeforeStart, AfterFinish, AfterTurn = allow, allow, allow
	BeforeTurn = func(context.Context, Summary) error { return nil }
	CheckWorker = func(context.Context, Summary, bool) (time.Duration, error) { return time.Minute, nil }
	issue.GitHubID, issue.RequirementsDigest = 42, strings.Repeat("a", 64)
	scope := protocol.RunScope{Device: "fixture", Project: "selected", Plan: "approved", Policy: 1}
	// A different workspace took the last slot after the advisory read.
	if _, err := query(`INSERT INTO journal_attempts VALUES('occupied','other','other/repo',1,1,'{"state":"running"}',1)`); err != nil {
		t.Fatal(err)
	}
	if a, err := StartPass(t.Context(), "personal", issue, scope, func(Summary) { t.Fatal("unexpected admission") }); !errors.Is(err, ErrTicketCapacity) || a.ID != "" {
		t.Fatalf("capacity result: %+v %v", a, err)
	}
	if _, err := query(`UPDATE journal_attempts SET data='{"state":"completed"}' WHERE id='occupied'`); err != nil {
		t.Fatal(err)
	}
	modelTurn = func(context.Context, *Attempt, string, string, any) (agent.Result, error) {
		return agent.Result{Text: `{"summary":"Confirm scope","disposition":"needs_owner","question":"Use the proposed label?"}`}, nil
	}
	starts := 0
	if a, err := StartPass(t.Context(), "personal", issue, scope, func(Summary) { starts++ }); err != nil || a.State != "waiting" || starts != 1 {
		t.Fatalf("fresh admission after capacity returned: %+v %v", a, err)
	}
}

func TestLateHostedGrantIsEndedBeforeModelWork(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	if _, err := SetLocalLimits(0, 1); err != nil {
		t.Fatal(err)
	}
	previousStart, previousEnd := BeforeTurn, AfterTurn
	t.Cleanup(func() { BeforeTurn, AfterTurn = previousStart, previousEnd })
	BeforeTurn = func(ctx context.Context, _ Summary) error { <-ctx.Done(); return nil }
	ended := 0
	AfterTurn = func(Summary) error { ended++; return nil }
	a := &Attempt{LocalRunScope: &protocol.RunScope{}, Project: Project{Config: Config{Stages: map[string]StageSettings{"plan": {TimeoutSeconds: 1}}}}, Summary: Summary{Phase: "plan"}}
	if release, err := reserveWorkerTurn(t.Context(), a); release != nil || !errors.Is(err, errModelCapacity) || ended != 1 {
		t.Fatalf("expired grant returned: released=%v ended=%d err=%v", release != nil, ended, err)
	}
	release, err := acquireModel("next-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
