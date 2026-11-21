// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
)

func TestIndependentlyPassingChangesFailInCombination(t *testing.T) {
	dir, issue := fixture(t)
	ctx := context.Background()
	for name, value := range map[string]string{"left.txt": "a\n", "right.txt": "b\n", "distinct.sh": "set -eu\ntest \"$(cat left.txt)\" != \"$(cat right.txt)\"\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mustGit := func(root string, args ...string) string {
		t.Helper()
		out, err := git(root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	mustGit(dir, "add", "--", "left.txt", "right.txt", "distinct.sh")
	mustGit(dir, "commit", "-m", "Distinct pair invariant")
	config := Config{Files: []string{"left.txt"}, Validate: []string{"/bin/sh", "distinct.sh"}}
	if err := AddProject("personal", issue.Repository, dir, config); err != nil {
		t.Fatal(err)
	}
	modelTurn = func(_ context.Context, a *Attempt, _ string, _ string, _ any) (agent.Result, error) {
		var text string
		switch a.Phase {
		case "plan":
			text = "Update the left value."
		case "implement":
			text = `{"summary":"Updated the left value","edits":[{"path":"left.txt","content":"c\n"}]}`
		case "review":
			text = `{"summary":"Independent check confirms two distinct values","findings":[]}`
		}
		return agent.Result{Text: text, Thread: "test-" + a.Phase}, nil
	}
	a, err := Start(ctx, "personal", issue.Repository, issue)
	if err != nil {
		t.Fatal(err)
	}
	a, err = Continue(ctx, a.ID, "")
	if err != nil || a.State != "completed" {
		t.Fatal(a.State, err)
	}
	mainRef := mustGit(dir, "symbolic-ref", "HEAD")
	positive, err := CheckCombined(ctx, a.ID, mainRef)
	if err != nil || positive.Status != "passed" || positive.ExitCode == nil || *positive.ExitCode != 0 || positive.TargetSHA != a.BaseSHA {
		t.Fatal(positive, err)
	}
	target := filepath.Join(t.TempDir(), "separate-target")
	mustGit(dir, "worktree", "add", "-b", "fixture-target", target, a.BaseSHA)
	if err = os.WriteFile(filepath.Join(target, "right.txt"), []byte("c\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mustGit(target, "commit", "-am", "Update right on target")
	local := a
	local.Worktree = target
	code, err := validate(ctx, &local)
	if err != nil || code == nil || *code != 0 {
		t.Fatal("target must pass independently", err)
	}
	targetSHA := mustGit(target, "rev-parse", "HEAD")
	receipt, err := CheckCombined(ctx, a.ID, "refs/heads/fixture-target")
	if err == nil || receipt.Status != "validation_failed" || receipt.ExitCode == nil || *receipt.ExitCode == 0 || receipt.CandidateSHA != a.CandidateSHA || receipt.TargetSHA != targetSHA || receipt.CombinedSHA == "" {
		t.Fatal("missing combined failure", receipt, err)
	}
	if mustGit(target, "rev-parse", "HEAD") != targetSHA || mustGit(a.Worktree, "rev-parse", "HEAD") != a.CandidateSHA || mustGit(dir, "rev-parse", "HEAD") != a.BaseSHA {
		t.Fatal("shared branch moved")
	}
	parents := mustGit(receipt.Worktree, "show", "-s", "--format=%P", "HEAD")
	if !strings.Contains(parents, receipt.CandidateSHA) || !strings.Contains(parents, receipt.TargetSHA) {
		t.Fatal("incorrect integration tree lineage")
	}
	checks, err := Checks(a.ID)
	if err != nil || len(checks) != 2 || checks[1].ID != receipt.ID {
		t.Fatal("missing durable integration evidence")
	}
}
func TestLocalModelCapacitySurvivesReentry(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	a, err := acquireModel("planner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := acquireModel("review")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acquireModel("implement"); err == nil {
		t.Fatal("third turn admitted")
	}
	if err = b(); err != nil {
		t.Fatal(err)
	}
	c, err := acquireModel("implement")
	if err != nil {
		t.Fatal(err)
	}
	if err = c(); err != nil {
		t.Fatal(err)
	}
	if err = a(); err != nil {
		t.Fatal(err)
	}
}

func TestDraftValidationRejectsCycleAndMissingAssumptions(t *testing.T) {
	p := `{"title":"Display fix","milestone":"Ready","assumptions":"Fixture covers rounding","tickets":[{"number":1,"title":"Fix display","criteria":["Correct minutes"],"verification":"Run regression","priority":1,"estimateHours":1,"dependencies":[]}]}`
	if _, err := parseDraft(p); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(p, `"dependencies":[]`, `"dependencies":[1]`, 1), strings.Replace(p, "Fixture covers rounding", "", 1), p + "{}", strings.Replace(p, "estimateHours", "hours", 1)} {
		if _, err := parseDraft(bad); err == nil {
			t.Fatal("invalid draft accepted")
		}
	}
}

func TestLocalTicketCapacityDoesNotReleaseLostContact(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	var attempts [3]Attempt
	for i := range attempts {
		id, err := newID()
		if err != nil {
			t.Fatal(err)
		}
		a := Attempt{Summary: Summary{ID: id, Workspace: "personal", Repository: "test/repo", IssueNumber: i + 1, State: "waiting", Version: 1}}
		if _, err = query(fmt.Sprintf("INSERT INTO journal_attempts VALUES(%s,'personal','test/repo',%d,1,%s,1);", literal(id), i+1, packed(a))); err != nil {
			t.Fatal(err)
		}
		a.State = "running"
		attempts[i] = a
	}
	if err := attempts[0].claimRunning(); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { results <- attempts[1].claimRunning() }()
	go func() { results <- attempts[2].claimRunning() }()
	wins := 0
	for range 2 {
		if <-results == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d won final local ticket slot", wins)
	}
	records, err := Summaries()
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, row := range records {
		if row.State == "running" {
			active++
		}
	}
	if active != 2 {
		t.Fatal("re-opened journal lost ticket ownership")
	}
}
