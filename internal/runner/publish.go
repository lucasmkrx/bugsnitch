// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

// CheckPublication verifies the paired device's current project assignment.
var CheckPublication func(context.Context, Summary) error

// PublishCandidate is an explicit local operator action. The destination and
// branch come from the reviewed attempt. No project-controlled Git settings
// or hooks are loaded by the network-facing Git process.
type PublicationReceipt struct {
	Branch   string `json:"branch"`
	Commit   string `json:"candidateSha"`
	Previous string `json:"previousSha,omitempty"`
	Undo     string `json:"undo,omitempty"`
}

func PublishCandidate(ctx context.Context, id string, version int, candidate string) (PublicationReceipt, error) {
	return publishCandidate(ctx, id, version, candidate, "", nil)
}

func PublishApprovedDraft(ctx context.Context, id string, version int, candidate, target string, confirm func(context.Context) error) (PublicationReceipt, error) {
	if !github.ValidBranch(target) || strings.HasPrefix(target, "bugsnitch/") || confirm == nil {
		return PublicationReceipt{}, errors.New("draft publication requires an approved target")
	}
	return publishCandidate(ctx, id, version, candidate, target, confirm)
}

func publishCandidate(ctx context.Context, id string, version int, candidate, target string, confirm func(context.Context) error) (PublicationReceipt, error) {
	var receipt PublicationReceipt
	unlock, err := attemptLock(id)
	if err != nil {
		return receipt, err
	}
	defer unlock()
	a, err := Get(id)
	if err != nil {
		return receipt, err
	}
	if a.ID != id || !idPattern.MatchString(id) || !repoPattern.MatchString(a.Repository) ||
		target != "" && a.Steward != nil || a.State != "completed" || a.Phase != "wrap_up" || a.Version != version ||
		candidate == "" || a.CandidateSHA != candidate || a.Branch != "bugsnitch/"+id {
		return receipt, errors.New("publication requires the exact completed attempt version and candidate SHA")
	}
	reviewBase := a.BaseSHA
	if a.Steward != nil {
		reviewBase = a.Steward.OriginalBase
	}
	depth, _, err := a.PassingReview(reviewBase)
	if err != nil {
		return receipt, err
	}
	required, err := candidateReviewDepth(ctx, &a, reviewBase)
	if err != nil {
		return receipt, err
	}
	if reviewLevel(depth) < reviewLevel(required) {
		return receipt, errors.New("candidate review does not meet its required depth")
	}
	current, err := project(a.Workspace, a.Repository)
	if err != nil || !reflect.DeepEqual(current, a.Project) {
		return receipt, errors.New("local checkout or approved configuration changed since review")
	}
	if err := a.cleanAt(candidate); err != nil {
		return receipt, err
	}
	if err := checkOrigin(a.Project.Path, a.Repository); err != nil {
		return receipt, err
	}
	if err := verifyIssue(ctx, a.Issue); err != nil {
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	receipt.Branch = a.Branch
	receipt.Commit = candidate
	if st := a.Steward; st != nil {
		receipt.Branch = "bugsnitch/" + st.RootAttempt
		receipt.Previous = st.PreviousHead
		receipt.Undo = "git diff --binary " + candidate + " " + st.PreviousHead + " | git apply --index && git commit -m \"Undo Bugsnitch PR correction\""
		if st.MergeCommit != "" {
			// Reverting the merged tree as a new commit could undo upstream work
			// on a later merge. Restoring only the exact PR ref avoids that.
			ref := "refs/heads/" + receipt.Branch
			receipt.Undo = "git push --force-with-lease=" + ref + ":" + candidate + " https://github.com/" + a.Repository + ".git " + st.PreviousHead + ":" + ref
		}
		pr, err := readStewardPR(ctx, a.Repository, st.Pull)
		if err != nil {
			return PublicationReceipt{}, err
		}
		if !pr.matches(a.Repository, receipt.Branch, st.PreviousHead, st.Target) || pr.ReviewDecision == "APPROVED" {
			if !pr.matches(a.Repository, receipt.Branch, candidate, st.Target) {
				return PublicationReceipt{}, errors.New("pull request moved, was approved, closed or queued")
			}
		}
	}
	if err = publishRef(ctx, a, "https://github.com/"+a.Repository+".git", "https", target, confirm); err != nil {
		return PublicationReceipt{}, err
	}
	return receipt, nil
}

type remoteGit func(...string) (string, error)

// Run network-facing Git in an empty bare repository. Only the reviewed
// repository's object store is shared, never its remote configuration or refs.
func withRemoteGit(ctx context.Context, a Attempt, protocol string, work func(remoteGit) error) error {
	objects, err := git(a.Worktree, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil || !filepath.IsAbs(objects) || strings.ContainsAny(objects, "\r\n") {
		return errors.New("could not locate reviewed Git objects")
	}
	private, err := os.MkdirTemp("", "bugsnitch-publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(private)
	env := []string{"PATH=" + os.Getenv("PATH"), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ALLOW_PROTOCOL=" + protocol, "GH_PROMPT_DISABLED=1",
		"GIT_NO_REPLACE_OBJECTS=1", "LC_ALL=C"}
	for _, key := range []string{"HOME", "GH_CONFIG_DIR", "GH_TOKEN", "GITHUB_TOKEN", "XDG_CONFIG_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull,
			"-c", "http.followRedirects=false", "-c", "credential.helper=!gh auth git-credential"}, args...)...)
		cmd.Dir, cmd.Env = private, env
		out := &capped{max: 2048}
		cmd.Stdout = out
		err := cmd.Run()
		return strings.TrimSpace(out.buffer.String()), err
	}
	format, err := git(a.Worktree, "rev-parse", "--show-object-format")
	if err != nil || (format != "sha1" && format != "sha256") {
		return errors.New("unsupported Git object format")
	}
	if _, err = run("init", "--bare", "--template=", "--object-format="+format); err != nil {
		return errors.New("could not prepare private Git publication")
	}
	env = append(env, "GIT_OBJECT_DIRECTORY="+objects)
	return work(run)
}

func publishRef(ctx context.Context, a Attempt, remote, protocol, target string, confirm func(context.Context) error) error {
	return withRemoteGit(ctx, a, protocol, func(run remoteGit) error {
		branch, expected := a.Branch, ""
		if a.Steward != nil {
			branch = "bugsnitch/" + a.Steward.RootAttempt
			expected = a.Steward.PreviousHead
			if _, err := run("merge-base", "--is-ancestor", expected, a.CandidateSHA); err != nil {
				return errors.New("correction must fast-forward the reviewed pull request head")
			}
		}
		ref := "refs/heads/" + branch
		exact := a.CandidateSHA + "\t" + ref
		read := func() (string, error) { return run("ls-remote", "--refs", "--", remote, ref) }
		current, err := read()
		if err != nil {
			return errors.New("could not inspect candidate branch; check GitHub CLI authentication and access")
		}
		if target != "" {
			targetRef := "refs/heads/" + target
			tip, err := run("ls-remote", "--refs", "--", remote, targetRef)
			if err != nil || tip != a.BaseSHA+"\t"+targetRef {
				return errors.New("draft target moved or is unavailable; inspect the current target before publication")
			}
		}
		if current == exact {
			return nil
		}
		if expected == "" && current != "" || expected != "" && current != expected+"\t"+ref {
			return errors.New("candidate branch already exists at a different revision")
		}
		if CheckPublication != nil {
			if err := CheckPublication(ctx, subject(&a)); err != nil {
				return err
			}
		}
		if confirm != nil {
			if err := confirm(ctx); err != nil {
				return err
			}
		}
		// The lease binds creation or fast-forward to the state previously reviewed.
		_, pushErr := run("push", "--porcelain", "--force-with-lease="+ref+":"+expected, "--", remote, a.CandidateSHA+":"+ref)
		current, err = read()
		if err == nil && current == exact {
			return nil
		}
		if pushErr != nil {
			return errors.New("publication was not confirmed; retry with the same attempt version to reconcile the remote branch")
		}
		return errors.New("published branch could not be confirmed at the reviewed revision")
	})
}
