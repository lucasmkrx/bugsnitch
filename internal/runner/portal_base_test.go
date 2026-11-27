// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestPortalTicketsStartFromCurrentRemoteTarget(t *testing.T) {
	checkout, issue := fixture(t)
	mustGit := func(dir string, args ...string) string {
		t.Helper()
		result, err := git(dir, args...)
		if err != nil {
			t.Fatal(args, err)
		}
		return result
	}
	initial := mustGit(checkout, "rev-parse", "HEAD")
	remote := filepath.Join(t.TempDir(), "remote.git")
	mustGit(checkout, "init", "--bare", remote)
	mustGit(checkout, "push", remote, initial+":refs/heads/release")
	// Redirect only this HTTPS fixture repository to a bare remote for the
	// isolated network-facing Git process. Keep the enrolled origin intact.
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := "#!/bin/sh\ncase \" $* \" in *' ls-remote '*|*' fetch '*) export GIT_ALLOW_PROTOCOL=file; exec '" + realGit + "' -c 'url.file://" + remote + ".insteadOf=https://github.com/" + issue.Repository + ".git' \"$@\";; esac\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	before, after, lease := BeforeTurn, AfterTurn, CheckWorker
	t.Cleanup(func() { BeforeTurn, AfterTurn, CheckWorker = before, after, lease })
	BeforeStart, AfterFinish, AfterTurn = func(Summary) error { return nil }, func(Summary) error { return nil }, func(Summary) error { return nil }
	BeforeTurn = func(context.Context, Summary) error { return nil }
	CheckWorker = func(context.Context, Summary, bool) (time.Duration, error) { return time.Minute, nil }
	modelTurn = func(context.Context, *Attempt, string, string, any) (agent.Result, error) {
		return agent.Result{Text: `{"summary":"Check cart behavior.","disposition":"needs_owner","question":"Which label?"}`}, nil
	}
	cfg, err := project("personal", issue.Repository)
	if err != nil {
		t.Fatal(err)
	}
	start := func(number int, expected string) {
		t.Helper()
		i := issue
		i.Number, i.GitHubID = number, int64(number)
		i.URL = fmt.Sprintf("https://github.com/%s/issues/%d", i.Repository, number)
		i.RequirementsDigest = strings.Repeat("a", 64)
		id := fmt.Sprintf("%032x", number)
		auth := UnattendedAuthority{GrantID: id, FirstAttemptID: id, PublishTarget: "release", Digest: projectDigest(cfg), TakeTurn: func(context.Context) error { return nil }}
		scope := protocol.RunScope{Device: "builder", Project: "project", Plan: "plan", Policy: 1}
		a, err := StartUnattendedPass(t.Context(), "personal", i, scope, auth, func(Summary) {})
		if err != nil || a.BaseSHA != expected || a.ID != id || a.State != "waiting" {
			t.Fatalf("start #%d: base %s expected %s state %s err=%v", number, a.BaseSHA, expected, a.State, err)
		}
		if got := mustGit(a.Worktree, "rev-parse", "HEAD"); got != expected {
			t.Fatal("worktree base", got)
		}
	}
	start(7, initial)
	// Simulate GitHub merging the first PR in another clone. The paired
	// checkout must remain on its old commit as the second ticket starts.
	other := filepath.Join(t.TempDir(), "other")
	mustGit(checkout, "clone", "--branch", "release", remote, other)
	mustGit(other, "commit", "--allow-empty", "-m", "Merge ticket 7")
	second := mustGit(other, "rev-parse", "HEAD")
	mustGit(other, "push", "origin", "HEAD:refs/heads/release")
	start(8, second)
	if got := mustGit(checkout, "rev-parse", "HEAD"); got != initial {
		t.Fatal("paired checkout moved", got)
	}
	if refs := mustGit(checkout, "for-each-ref", "--format=%(refname)", "refs/remotes/"); refs != "" {
		t.Fatal("tracking refs changed", refs)
	}
}
