// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
)

func fixture(t *testing.T) (string, Issue) {
	t.Helper()
	root := t.TempDir()
	dir, err := canonical(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUGSNITCH_HOME", filepath.Join(t.TempDir(), "private"))
	if _, err = git(dir, "init"); err != nil {
		t.Fatal(err)
	}
	for p, text := range map[string]string{"example.txt": "broken\n", "check.sh": "#!/bin/sh\nset -eu\ntest \"$(cat example.txt)\" = fixed\ntest -z \"${BUGSNITCH_GITHUB_READ_TOKEN:-}\"\n"} {
		if err = os.WriteFile(filepath.Join(dir, p), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "--", "example.txt", "check.sh"}, {"commit", "-m", "baseline"}, {"remote", "add", "origin", "https://github.com/fixture/selected.git"}} {
		if _, err = git(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	config := Config{Files: []string{"example.txt"}, Validate: []string{"/bin/sh", "check.sh"}}
	if err = AddProject("personal", "fixture/selected", dir, config); err != nil {
		t.Fatal(err)
	}
	oldVerify := verifyIssue
	oldModel := modelTurn
	oldBefore := BeforeStart
	oldAfter := AfterFinish
	t.Cleanup(func() { verifyIssue = oldVerify; modelTurn = oldModel; BeforeStart = oldBefore; AfterFinish = oldAfter })
	verifyIssue = func(context.Context, Issue) error { return nil }
	return dir, Issue{Schema: 1, Repository: "fixture/selected", Number: 7, URL: "https://github.com/fixture/selected/issues/7", Title: "Synthetic bug", State: "open", UpdatedAt: "2026-11-21T00:00:00Z", Criteria: []string{"example.txt contains fixed"}}
}
func TestTrustedPolicyRunsTwoIndependentDeepReviews(t *testing.T) {
	dir, issue := fixture(t)
	if err := os.Mkdir(filepath.Join(dir, ".bugsnitch"), 0700); err != nil {
		t.Fatal(err)
	}
	policy := `{"schema":1,"policyOwners":["lead"],"default":{"reviewDepth":"quick","checks":[{"name":"CI","appId":19}]},"rules":[{"name":"customer behavior","paths":["example.txt"],"reviewDepth":"deep","owners":[],"checks":[]}]}`
	if err := os.WriteFile(filepath.Join(dir, ".bugsnitch", "approvals.json"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "add", "--", ".bugsnitch/approvals.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "commit", "-m", "Require deep review for customer behavior"); err != nil {
		t.Fatal(err)
	}
	reviews := 0
	modelTurn = func(_ context.Context, a *Attempt, instruction, payload string, _ any) (agent.Result, error) {
		out := agent.Result{Thread: a.Phase, Turn: "turn", Model: "fixture", Text: "Inspect the example."}
		switch a.Phase {
		case "implement":
			out.Text = `{"summary":"Correct value","edits":[{"path":"example.txt","content":"fixed\n"}]}`
		case "review":
			reviews++
			out.Thread = fmt.Sprintf("independent-%d", reviews)
			out.Text = `{"summary":"Reviewed the threshold","findings":[]}`
			if reviews == 2 && (!strings.Contains(instruction, "boundary inputs") || strings.Contains(payload, "Reviewed the threshold")) {
				t.Fatal("second reviewer must receive its own assignment and the original evidence")
			}
		}
		return out, nil
	}
	a, err := Start(t.Context(), "personal", issue.Repository, issue)
	if err == nil {
		a, err = Continue(t.Context(), a.ID, "Apply the fix.")
	}
	if err != nil || a.State != "completed" || reviews != 2 {
		t.Fatalf("state=%s reviews=%d err=%v", a.State, reviews, err)
	}
	depth, id, err := a.PassingReview(a.BaseSHA)
	if err != nil || depth != "deep" || id != a.Artifacts[len(a.Artifacts)-1].ID {
		t.Fatal(depth, id, err)
	}
	a.Artifacts[len(a.Artifacts)-1].SecondReviewOf = "unrelated review"
	if _, _, err = a.PassingReview(a.BaseSHA); err == nil {
		t.Fatal("accepted an unrelated first review")
	}
}

func TestIssueWorkflowReplayAndIndependentReview(t *testing.T) {
	dir, issue := fixture(t)
	ctx := context.Background()
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "synthetic-credential")
	if _, err := exec.Command("/bin/sh", "-c", "cd \"$1\" && /bin/sh check.sh", "test", dir).Output(); err == nil {
		t.Fatal("regression checker passes on broken baseline")
	}
	var roles []string
	BeforeStart = func(s Summary) error {
		if s.CriteriaHash != domain.CriteriaHash(issue.Criteria) || s.LocalPath != dir {
			t.Fatal("wrong admission subject")
		}
		return nil
	}
	modelTurn = func(_ context.Context, a *Attempt, _ string, prompt string, _ any) (agent.Result, error) {
		roles = append(roles, a.Phase)
		var out string
		switch a.Phase {
		case "plan":
			out = "Check the fixture's expected value."
		case "implement":
			if !strings.Contains(prompt, "Use fixed") {
				t.Fatal("feedback not carried forward")
			}
			out = `{"summary":"Corrected value","edits":[{"path":"example.txt","content":"fixed\n"}]}`
		case "review":
			if !strings.Contains(prompt, "-broken") || !strings.Contains(prompt, "+fixed") || !strings.Contains(prompt, "ExitCode") {
				t.Fatal("review did not receive diff and evidence")
			}
			out = `{"summary":"Change meets criterion","findings":[]}`
		}
		return agent.Result{Thread: "fixture-" + a.Phase, Text: out}, nil
	}
	a, err := Start(ctx, "personal", issue.Repository, issue)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != "waiting" || a.Phase != "implement" || a.Worktree == dir {
		t.Fatalf("invalid plan result %+v", a.Summary)
	}
	duplicate, err := Start(ctx, "personal", issue.Repository, issue)
	if err == nil || duplicate.ID != a.ID || len(roles) != 1 {
		t.Fatal("repeated issue created another attempt")
	}
	// Registration after a side effect may be retried by the same attempt and reuses its exact tree.
	if err = a.prepare(); err != nil {
		t.Fatal(err)
	}
	a, err = Continue(ctx, a.ID, "Use fixed, preserve validation.")
	if err != nil {
		t.Fatal(err)
	}
	if a.State != "completed" || a.CandidateSHA == a.BaseSHA || len(a.Artifacts) != 4 || !strings.Contains(a.PRBody, issue.URL) {
		t.Fatal("missing validated PR candidate")
	}
	if len(a.Artifacts[2].ID) != 64 || a.Artifacts[2].ID == a.Artifacts[3].ID || a.Artifacts[2].ExitCode == nil || *a.Artifacts[2].ExitCode != 0 || a.Artifacts[1].Thread == a.Artifacts[3].Thread || a.Artifacts[3].CandidateSHA != a.CandidateSHA {
		t.Fatal("independent review not bound to candidate")
	}
	main, _ := os.ReadFile(filepath.Join(dir, "example.txt"))
	if string(main) != "broken\n" {
		t.Fatal("owner checkout changed")
	}
	records, err := Summaries()
	if err != nil || len(records) != 1 || len(records[0].Evidence) != 4 {
		t.Fatalf("sync summaries: %v %v", records, err)
	}
	b, _ := json.Marshal(records)
	for _, s := range []string{"synthetic-credential", "Corrected value", "example.txt", dir} {
		if strings.Contains(string(b), s) {
			t.Fatal("private data leaked in device summary")
		}
	}
}
func TestControlsRemainPendingUntilQuiescence(t *testing.T) {
	_, issue := fixture(t)
	began := make(chan string)
	end := make(chan struct{})
	result := make(chan Attempt, 1)
	failure := make(chan error, 1)
	modelTurn = func(_ context.Context, a *Attempt, _ string, _ string, _ any) (agent.Result, error) {
		began <- a.ID
		<-end
		return agent.Result{}, errors.New("model process ended")
	}
	go func() {
		a, err := Start(context.Background(), "personal", issue.Repository, issue)
		result <- a
		failure <- err
	}()
	var id string
	select {
	case id = <-began:
	case <-time.After(5 * time.Second):
		t.Fatal("plan did not start")
	}
	live, err := Get(id)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		ack, err := ApplyControl("control-001", id, "stop", live.Version)
		if err != nil || ack != "received" {
			t.Fatal(ack, err)
		}
	}
	check, _ := Get(id)
	if check.State != "running" {
		t.Fatal("receipt claimed termination")
	}
	close(end)
	done := <-result
	if err = <-failure; err != nil || done.State != "cancelled" {
		t.Fatal(done.State, err)
	}
	ack, err := ApplyControl("control-001", id, "stop", live.Version)
	if err != nil || ack != "completed" {
		t.Fatal(ack, err)
	}
	if _, err = Continue(context.Background(), id, ""); err == nil {
		t.Fatal("stopped attempt resumed")
	}
}
func TestDirtyAndSwappedWorktreesPreserved(t *testing.T) {
	dir, issue := fixture(t)
	modelTurn = func(context.Context, *Attempt, string, string, any) (agent.Result, error) {
		return agent.Result{Text: "plan"}, nil
	}
	a, err := Start(context.Background(), "personal", issue.Repository, issue)
	if err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(a.Worktree, "user-notes.txt")
	if err = os.WriteFile(note, []byte("private note"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Continue(context.Background(), a.ID, ""); err == nil {
		t.Fatal("accepted dirty worktree")
	}
	if _, err = os.Stat(note); err != nil {
		t.Fatal("dirty file lost")
	}
	a.Worktree = dir
	if err = a.checkTree(); err == nil {
		t.Fatal("accepted owner checkout")
	}
}

func TestCommitBeforeJournalAcknowledgment(t *testing.T) {
	_, issue := fixture(t)
	ctx := context.Background()
	modelTurn = func(context.Context, *Attempt, string, string, any) (agent.Result, error) {
		return agent.Result{Text: "plan"}, nil
	}
	a, err := Start(ctx, "personal", issue.Repository, issue)
	if err != nil {
		t.Fatal(err)
	}
	// Persist the exact state left when git commit succeeded but the runner
	// exited before recording its result. The commit itself is real.
	a.State = "running"
	a.Intent = &CandidateIntent{Parent: a.BaseSHA, SHA256: map[string]string{"example.txt": fmt.Sprintf("%x", sha256.Sum256([]byte("fixed\n")))}, Artifact: Artifact{Phase: "implement", Summary: "Fix the example."}}
	if err = a.save(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(a.Worktree, "example.txt"), []byte("fixed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = RecoverCommit(a.ID); err == nil {
		t.Fatal("partially applied edits accepted")
	}
	if _, err = git(a.Worktree, "commit", "-am", "fixture side effect"); err != nil {
		t.Fatal(err)
	}
	head, _ := git(a.Worktree, "rev-parse", "HEAD")
	release, err := attemptLock(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RecoverCommit(a.ID); err == nil {
		t.Fatal("recovered a live owner")
	}
	release()
	a, err = RecoverCommit(a.ID)
	if err != nil || a.Phase != "validate" || a.State != "waiting" || a.CandidateSHA != head {
		t.Fatal(a.Summary, err)
	}
	again, err := RecoverCommit(a.ID)
	if err != nil || again.Version != a.Version || len(again.Artifacts) != len(a.Artifacts) {
		t.Fatal("duplicate reconciliation wrote another artifact")
	}
	modelTurn = func(context.Context, *Attempt, string, string, any) (agent.Result, error) {
		return agent.Result{Text: `{"summary":"Inspected fixed value","findings":[]}`}, nil
	}
	done, err := Continue(ctx, a.ID, "Validate the retained candidate.")
	if err != nil || done.State != "completed" || done.CandidateSHA != head {
		t.Fatal(done.Summary, err)
	}
	ignore, err := git(done.Worktree, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(ignore, []byte("*.local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(done.Worktree, "notes.local")
	if err = os.WriteFile(keep, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Cleanup(done.ID); err == nil {
		t.Fatal("ignored user work lost")
	}
	if err = os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	if err = Cleanup(done.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(done.Worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("clean worktree retained")
	}
	if _, err = git(done.Project.Path, "rev-parse", done.Branch); err != nil {
		t.Fatal("retained branch removed")
	}
}

func TestBoundedNewAttemptAfterExplicitRetry(t *testing.T) {
	_, issue := fixture(t)
	ctx := context.Background()
	calls := 0
	modelTurn = func(context.Context, *Attempt, string, string, any) (agent.Result, error) {
		calls++
		return agent.Result{}, errors.New("ordinary task failure")
	}
	a, err := Start(ctx, "personal", issue.Repository, issue)
	if err == nil || a.State != "failed" {
		t.Fatal("expected failed attempt")
	}
	b, err := Retry(ctx, a.ID, issue, "Missing test fixture restored")
	if err == nil || b.State != "failed" || b.ID == a.ID || b.PreviousAttempt != a.ID || b.Worktree == a.Worktree {
		t.Fatal("retry did not have distinct identity and worktree")
	}
	replay, err := Retry(ctx, a.ID, issue, "Missing test fixture restored")
	if err == nil || replay.ID != b.ID || calls != 2 {
		t.Fatal("replayed authorization made another attempt")
	}
	c, err := Retry(ctx, b.ID, issue, "Owner reviewed the fixture")
	if err == nil || c.State != "failed" || c.ID == b.ID {
		t.Fatal("final retry not distinct")
	}
	latest, err := Retry(ctx, c.ID, issue, "Retry")
	if err == nil || latest.ID != c.ID || calls != 3 {
		t.Fatal("attempt budget exceeded")
	}
	records, err := Summaries()
	if err != nil || len(records) != 3 {
		t.Fatal("historical attempts lost")
	}
}
func TestLegacyJournalMigrationRetainsControl(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	dir, err := home()
	if err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "runner.sqlite")
	id := strings.Repeat("c", 32)
	a := Attempt{Summary: Summary{ID: id, Workspace: "personal", Repository: "example/app", IssueNumber: 5, Phase: "plan", State: "cancelled", BaseSHA: strings.Repeat("e", 40), Version: 2}}
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is required to create the pre-migration journal fixture")
	}
	err = exec.Command(sqlite, "-batch", "-bail", "-init", os.DevNull, db, fmt.Sprintf("CREATE TABLE local_attempts(id TEXT PRIMARY KEY,workspace TEXT,repo TEXT,issue INTEGER,version INTEGER,data TEXT);CREATE TABLE control_receipts(command_id TEXT PRIMARY KEY,attempt_id TEXT,op TEXT,subject_version INTEGER,status TEXT);INSERT INTO local_attempts VALUES(%s,'personal','example/app',5,2,%s);INSERT INTO control_receipts VALUES('receipt',%s,'stop',1,'completed');", literal(id), packed(a), literal(id))).Run()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Get(id)
	if err != nil || restored.State != "cancelled" {
		t.Fatal(restored, err)
	}
	ack, err := ApplyControl("receipt", id, "stop", 1)
	if err != nil || ack != "completed" {
		t.Fatal(ack, err)
	}
}

func TestGitLiteralPathsAndOutputLimit(t *testing.T) {
	dir, _ := fixture(t)
	const selected = "café[1].txt"
	lookalike := "café1.txt"
	for _, name := range []string{selected, lookalike} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := git(dir, "add", "--", selected, lookalike); err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "commit", "-m", "fixture names"); err != nil {
		t.Fatal(err)
	}
	a := Attempt{Worktree: dir, Project: Project{Config: Config{Files: []string{selected}}}}
	got, err := a.contextFiles()
	if err != nil || got[selected] != selected {
		t.Fatalf("context: %v %v", got, err)
	}
	for _, name := range []string{selected, lookalike} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := git(dir, "add", "--", selected); err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "commit", "-m", "only the literal file"); err != nil {
		t.Fatal(err)
	}
	names, err := changedPaths(dir, "HEAD~", "HEAD")
	if err != nil || len(names) != 1 || names[0] != selected {
		t.Fatalf("changed names: %q %v", names, err)
	}
	if err := os.WriteFile(filepath.Join(dir, selected), []byte(strings.Repeat("x", 512<<10)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "diff", "--", selected); err == nil || !strings.Contains(err.Error(), "artifact limit") {
		t.Fatalf("large diff: %v", err)
	}
}
