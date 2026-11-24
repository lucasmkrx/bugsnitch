// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPublicationCreatesOnlyReviewedRefAndReconciles(t *testing.T) {
	dir, _ := fixture(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git(dir, "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	before, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = git(dir, "commit", "--allow-empty", "-m", "Reviewed improvement"); err != nil {
		t.Fatal(err)
	}
	head, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	a := Attempt{Summary: Summary{CandidateSHA: head}, Worktree: dir, Branch: "bugsnitch/test"}
	saved := CheckPublication
	t.Cleanup(func() { CheckPublication = saved })
	CheckPublication = func(context.Context, Summary) error { return ErrAuthorityStale }
	if err = publishRef(t.Context(), a, remote, "file", "", nil); !errors.Is(err, ErrAuthorityStale) {
		t.Fatal("paused publication", err)
	}
	if _, err := git(remote, "show-ref"); err == nil {
		t.Fatal("branch published after revoked authorization")
	}
	CheckPublication = saved
	for range 2 {
		if err = publishRef(t.Context(), a, remote, "file", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := git(remote, "show-ref"); err != nil || got != head+" refs/heads/bugsnitch/test" {
		t.Fatal(got, err)
	}
	if _, err = git(remote, "update-ref", "refs/heads/bugsnitch/test", before); err != nil {
		t.Fatal(err)
	}
	if err = publishRef(t.Context(), a, remote, "file", "", nil); err == nil {
		t.Fatal("moved remote branch accepted")
	}
	if got, err := git(remote, "rev-parse", "refs/heads/bugsnitch/test"); err != nil || got != before {
		t.Fatal("remote overwritten", got, err)
	}
}
