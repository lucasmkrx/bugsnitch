// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
)

func TestStewardRepairsPinnedPullAndRereviewsEntireChange(t *testing.T) {
	dir, issue := fixture(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "notes.txt"}, {"commit", "-m", "Base notes"}} {
		if _, err := git(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddProject("personal", issue.Repository, dir, Config{Files: []string{"example.txt", "notes.txt"}, Validate: []string{"/bin/sh", "check.sh"}}); err != nil {
		t.Fatal(err)
	}
	modelTurn = func(_ context.Context, a *Attempt, _ string, input string, _ any) (agent.Result, error) {
		s := ""
		switch a.Phase {
		case "plan":
			s = "Check shipping behavior"
			if a.Steward != nil {
				s = `{"disposition":"ready","question":"","summary":"Repair unaddressed PR feedback and run existing checks"}`
				if !strings.Contains(input, "Unresolved review") && !strings.Contains(input, "Resolve each recorded text conflict") {
					t.Fatal("missing PR evidence", input)
				}
			}
		case "implement":
			s = `{"summary":"Correct the feature","edits":[{"path":"example.txt","content":"fixed\n"}]}`
			if a.Steward != nil {
				s = `{"summary":"Address the review","edits":[{"path":"notes.txt","content":"verified\n"}]}`
			}
		case "review":
			if a.Steward != nil && (!strings.Contains(input, "notes.txt") || !strings.Contains(input, "example.txt")) {
				t.Fatal("review omitted original change", input)
			}
			s = `{"summary":"Independent read succeeded","findings":[]}`
		}
		return agent.Result{Text: s, Thread: "test-" + a.Phase, Turn: "turn", Model: "fixture"}, nil
	}
	ctx := t.Context()
	a, err := Start(ctx, "personal", issue.Repository, issue)
	if err != nil {
		t.Fatal(err)
	}
	a, err = Continue(ctx, a.ID, "")
	if err != nil || a.State != "completed" {
		t.Fatal(a.State, err)
	}
	bins := t.TempDir()
	if err = os.WriteFile(filepath.Join(bins, "gh"), []byte("#!/bin/sh\ncat \"$(dirname \"$0\")/pr.json\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bins+string(os.PathListSeparator)+os.Getenv("PATH"))
	type obj = map[string]any
	pull := obj{"number": 18, "headRepository": obj{"nameWithOwner": issue.Repository}, "headRefName": a.Branch, "baseRefName": "main", "headRefOid": a.CandidateSHA, "state": "OPEN", "reviewDecision": "CHANGES_REQUESTED",
		"reviewThreads": obj{"nodes": []obj{{"isResolved": false, "isOutdated": false, "comments": obj{"nodes": []obj{{"bodyText": "Verify the feature in the notes."}}}}}}}
	write := func() {
		raw, _ := json.Marshal(obj{"data": obj{"repository": obj{"pullRequest": pull}}})
		if err := os.WriteFile(filepath.Join(bins, "pr.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err = git(dir, "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	// Route only the enrolled HTTPS endpoint to a local bare Git remote.
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \" $* \" in *' push '*|*' ls-remote '*) export GIT_ALLOW_PROTOCOL=file; exec '" + realGit + "' -c 'url.file://" + remote + ".insteadOf=https://github.com/" + issue.Repository + ".git' \"$@\";; esac\nexec '" + realGit + "' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bins, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	confirmations := 0
	check := func(context.Context) error { confirmations++; return nil }
	if _, err = PublishApprovedDraft(ctx, a.ID, a.Version, a.CandidateSHA, "main", check); err == nil || confirmations != 0 {
		t.Fatal("missing remote target accepted", err)
	}
	if _, err = git(dir, "push", remote, a.BaseSHA+":refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishApprovedDraft(ctx, a.ID, a.Version, a.CandidateSHA, "main", func(context.Context) error { return ErrAuthorityStale }); !errors.Is(err, ErrAuthorityStale) {
		t.Fatal("withdrawn publication accepted", err)
	}
	if got, _ := git(remote, "show-ref", "--verify", "refs/heads/"+a.Branch); got != "" {
		t.Fatal("published after withdrawal", got)
	}
	if _, err = PublishApprovedDraft(ctx, a.ID, a.Version, a.CandidateSHA, "main", check); err != nil || confirmations != 1 {
		t.Fatal("publish authorized draft branch", confirmations, err)
	}
	corrected, err := Steward(ctx, a.ID, a.Version, a.CandidateSHA, 18)
	if err != nil || corrected.State != "completed" || corrected.Steward == nil || corrected.BaseSHA != a.CandidateSHA {
		t.Fatal(corrected.State, corrected.Message, err)
	}
	receipt, err := PublishCandidate(ctx, corrected.ID, corrected.Version, corrected.CandidateSHA)
	if err != nil || receipt.Previous != a.CandidateSHA || !strings.Contains(receipt.Undo, "git diff --binary") {
		t.Fatal("publish correction", receipt, err)
	}
	if tip, err := git(remote, "rev-parse", "refs/heads/"+a.Branch); err != nil || tip != corrected.CandidateSHA {
		t.Fatal(tip, err)
	}
	if saved, err := Get(a.ID); err != nil || saved.Version != a.Version || saved.CandidateSHA != a.CandidateSHA {
		t.Fatal("completed original changed")
	}
	if _, err = Steward(ctx, a.ID, a.Version, a.CandidateSHA, 18); err == nil {
		t.Fatal("duplicated a correction")
	}
	pull["headRefOid"] = corrected.CandidateSHA
	pull["reviewDecision"] = "CHANGES_REQUESTED"
	write()
	if err = os.WriteFile(filepath.Join(bins, "gh"), []byte(`#!/bin/sh
fixture=$(dirname "$0")
case "$*" in
  *BugsnitchNotes*) if test -f "$fixture/posted"; then cat "$fixture/existing"; else cat "$fixture/empty"; fi ;;
  *"--method POST"*) echo x >> "$fixture/posted"; echo '{}' ;;
  *) cat "$fixture/pr.json" ;;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	marker := "<!-- bugsnitch:steward:" + corrected.ID + ":" + corrected.CandidateSHA + " -->"
	for file, nodes := range map[string][]obj{"empty": {}, "existing": {{"body": "Corrections. " + marker, "viewerDidAuthor": true}}} {
		data, _ := json.Marshal(obj{"data": obj{"repository": obj{"pullRequest": obj{"comments": obj{"nodes": nodes, "pageInfo": obj{"hasPreviousPage": false}}}}}})
		if err = os.WriteFile(filepath.Join(bins, file), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err = PostStewardNote(ctx, corrected.ID, receipt); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(bins, "posted"))
	if err != nil || string(b) != "x\n" {
		t.Fatal("duplicate note", err)
	}
	pull["reviewDecision"] = "APPROVED"
	write()
	if _, err = Steward(ctx, corrected.ID, corrected.Version, corrected.CandidateSHA, 18); err == nil {
		t.Fatal("steward changed signed head")
	}
	// The target changes the same line as the first correction. The second
	// correction sees both sides, reviews the PR against the new target and
	// produces a commit with both the target and prior PR in its ancestry.
	if err = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("server-side change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = git(dir, "commit", "-am", "Concurrent target update"); err != nil {
		t.Fatal(err)
	}
	target, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	pull["reviewDecision"] = "CHANGES_REQUESTED"
	pull["mergeable"] = "CONFLICTING"
	pull["baseRefOid"] = target
	write()
	resolved, err := Steward(ctx, corrected.ID, corrected.Version, corrected.CandidateSHA, 18)
	if err != nil || resolved.State != "completed" || resolved.Steward.MergeCommit == "" {
		t.Fatal("conflict repair", resolved.State, resolved.Message, err)
	}
	for _, ancestor := range []string{target, corrected.CandidateSHA} {
		if _, err = git(dir, "merge-base", "--is-ancestor", ancestor, resolved.CandidateSHA); err != nil {
			t.Fatal("lost merge ancestry", ancestor, err)
		}
	}
	receipt, err = PublishCandidate(ctx, resolved.ID, resolved.Version, resolved.CandidateSHA)
	if err != nil || receipt.Previous != corrected.CandidateSHA || !strings.Contains(receipt.Undo, "--force-with-lease=refs/heads/"+a.Branch+":"+resolved.CandidateSHA) {
		t.Fatal("publish conflict resolution", receipt, err)
	}
	pull["headRefOid"] = resolved.CandidateSHA
	pull["mergeable"] = "MERGEABLE"
	write()
	if _, err = Steward(ctx, resolved.ID, resolved.Version, resolved.CandidateSHA, 18); err == nil {
		t.Fatal("exceeded correction budget")
	}

}
