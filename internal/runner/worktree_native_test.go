// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the same Rust worktree operations that Start and Cleanup call,
// using a real repository and a runner data directory with spaces.
func TestNativeWorktreeGuards(t *testing.T) {
	project, issue := fixture(t)
	t.Setenv("BUGSNITCH_HOME", filepath.Join(t.TempDir(), "runner data"))
	dataDir, err := home()
	if err != nil {
		t.Fatal(err)
	}
	base, err := git(project, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	a := Attempt{
		Summary: Summary{ID: id, Repository: issue.Repository, BaseSHA: base},
		Project: Project{Path: project}, Branch: "bugsnitch/" + id,
		Worktree: filepath.Join(dataDir, "worktrees", id),
	}
	if err := a.prepare(); err != nil {
		t.Fatal(err)
	}

	admin, err := git(a.Worktree, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	backpointer := filepath.Join(admin, "gitdir")
	original, err := os.ReadFile(backpointer)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(backpointer, []byte(filepath.Join(project, ".git")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = a.checkTree(); err == nil {
		t.Fatal("accepted a mismatched Git worktree registration")
	}
	if err = os.WriteFile(backpointer, original, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err = git(a.Worktree, "update-index", "--assume-unchanged", "--", "example.txt"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(a.Worktree, "example.txt")
	if err = os.WriteFile(file, []byte("owner edits"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = a.worktreeOp("cleanup", base); err == nil {
		t.Fatal("cleaned a tree with hidden owner edits")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "owner edits" {
		t.Fatal("owner edits lost")
	}
	if _, err = git(a.Worktree, "update-index", "--no-assume-unchanged", "--", "example.txt"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte("broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate the clean index left by an in-progress cherry-pick that
	// produced an empty patch. The owner's sequence must remain recoverable.
	operation := filepath.Join(admin, "CHERRY_PICK_HEAD")
	if err = os.WriteFile(operation, []byte(base+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = a.worktreeOp("cleanup", base); err == nil {
		t.Fatal("discarded an in-progress cherry-pick")
	}
	if err = os.Remove(operation); err != nil {
		t.Fatal("lost cherry-pick state", err)
	}

	if err = a.worktreeOp("cleanup", base); err != nil {
		t.Fatal(err)
	}
	if err = a.worktreeOp("cleanup", base); err != nil {
		t.Fatal("cleanup replay:", err)
	}
	listing, err := git(project, "worktree", "list", "--porcelain")
	if err != nil || strings.Contains(listing, a.Worktree) {
		t.Fatal("removed worktree still registered", err)
	}
	if head, err := git(project, "rev-parse", "refs/heads/"+a.Branch); err != nil || head != base {
		t.Fatal("cleanup changed branch")
	}
}
