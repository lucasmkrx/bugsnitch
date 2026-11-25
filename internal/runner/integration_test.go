// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	measured := map[string]bool{}
	for _, e := range a.Evidence {
		if e.DurationMS != nil && *e.DurationMS >= 0 {
			measured[e.Phase] = true
		}
	}
	for _, phase := range []string{"plan", "implement", "validate", "review"} {
		if !measured[phase] {
			t.Fatalf("missing measured activity duration for %s", phase)
		}
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
	independent := exec.CommandContext(ctx, "/bin/sh", "distinct.sh")
	independent.Dir = target
	if err = independent.Run(); err != nil {
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

	t.Run("retain inspected base without moving user refs", func(t *testing.T) {
		ref, err := RetainInspectedBase(ctx, a, "fixture-target", targetSHA)
		if err != nil || mustGit(dir, "rev-parse", ref) != targetSHA {
			t.Fatal(ref, err)
		}
		check := receipt
		check.TargetRef = ref
		got, err := integrationGit(ctx, "resolve", check, a)
		if err != nil || got.TargetSHA != targetSHA {
			t.Fatal(got, err)
		}
		if mustGit(dir, "rev-parse", "HEAD") != a.BaseSHA || mustGit(target, "rev-parse", "HEAD") != targetSHA {
			t.Fatal("retaining the inspection moved a user ref")
		}
	})

	t.Run("target movement invalidates combination", func(t *testing.T) {
		mustGit(dir, "commit", "--allow-empty", "-m", "Move integration target")
		verified, err := integrationGit(ctx, "verify", positive, a)
		if err == nil || verified.Status != "stale" {
			t.Fatal("moved target accepted for existing integration check", verified, err)
		}
	})

	t.Run("cleanup preserves ignored and unrelated work and reconciles a lost acknowledgment", func(t *testing.T) {
		// Git's default status hides ignored data. Cleanup must inspect it explicitly.
		exclude := filepath.Join(dir, ".git", "info", "exclude")
		f, err := os.OpenFile(exclude, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.WriteString("\nprivate-build-output\n"); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		ignored := filepath.Join(positive.Worktree, "private-build-output")
		if err = os.WriteFile(ignored, []byte("retained"), 0600); err != nil {
			t.Fatal(err)
		}
		if err = CleanupIntegration(ctx, a.ID, positive.ID); err == nil {
			t.Fatal("removed checkout with ignored build output")
		}
		if b, err := os.ReadFile(ignored); err != nil || string(b) != "retained" {
			t.Fatal("lost ignored output", err)
		}
		if err = os.Remove(ignored); err != nil {
			t.Fatal(err)
		}

		// Interrupt after native removal but before the Go receipt update.
		removed, err := integrationGit(ctx, "cleanup", positive, a)
		if err != nil || mustGit(dir, "rev-parse", "--verify", removed.ArchiveRef) != positive.CombinedSHA {
			t.Fatal("combined commit was not retained", err)
		}
		if _, err = os.Lstat(positive.Worktree); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("original checkout still present", err)
		}
		// Even a newly registered checkout at this exact path and commit has no
		// ownership receipt from the original check.
		mustGit(dir, "worktree", "add", "--detach", "--", positive.Worktree, positive.CombinedSHA)
		if err = CleanupIntegration(ctx, a.ID, positive.ID); err == nil {
			t.Fatal("replayed cleanup removed another checkout")
		}
		if head := mustGit(positive.Worktree, "rev-parse", "HEAD"); head != positive.CombinedSHA {
			t.Fatal("replacement checkout changed")
		}
		mustGit(dir, "worktree", "remove", "--", positive.Worktree)
		if err = CleanupIntegration(ctx, a.ID, positive.ID); err != nil {
			t.Fatal(err)
		}
		checks, err := Checks(a.ID)
		if err != nil || len(checks) != 2 || checks[0].Status != "passed" || checks[0].CleanedAt == "" || checks[0].ArchiveRef != removed.ArchiveRef {
			t.Fatal("receipt did not reconcile after native cleanup", err)
		}
		if err = CleanupIntegration(ctx, a.ID, positive.ID); err != nil {
			t.Fatal("second cleanup", err)
		}
		if _, err = os.Stat(receipt.Worktree); err != nil {
			t.Fatal("unrelated check removed", err)
		}
	})
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
