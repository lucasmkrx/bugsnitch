// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var stewardOID = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)
var mergeMarker = regexp.MustCompile(`(?m)^(<{7,}( |$)|={7,}$|>{7,}( |$)|\|{7,}( |$))`)

func (a Attempt) changeBase() string {
	if a.Steward != nil && a.Steward.MergeCommit != "" {
		return a.Steward.OriginalBase
	}
	return a.BaseSHA
}

// merge-tree writes only Git objects. Keep the conflict-marked tree out of the
// published ref; the model, validator and independent reviewer see the actual
// merged checkout at the new target. Move/delete and binary conflicts stop.
func stewardMerge(ctx context.Context, a Attempt, conf Config, target string) (string, []string, error) {
	if !stewardOID.MatchString(target) || !stewardOID.MatchString(a.CandidateSHA) {
		return "", nil, errors.New("conflicting PR has no exact target or head")
	}
	if _, err := git(a.Worktree, "cat-file", "-e", target+"^{commit}"); err != nil {
		fetch, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err = withRemoteGit(fetch, a.Worktree, "https", func(run remoteGit) error {
			_, err := run("-c", "fetch.fsckObjects=true", "fetch", "--no-write-fetch-head", "--no-auto-maintenance", "--no-tags", "--recurse-submodules=no", "--", "https://github.com/"+a.Repository+".git", target)
			return err
		}); err != nil {
			return "", nil, errors.New("could not fetch the exact GitHub target object for conflict resolution")
		}
		if _, err = git(a.Worktree, "cat-file", "-e", target+"^{commit}"); err != nil {
			return "", nil, errors.New("fetched target commit could not be verified")
		}
	}
	out, err := gitAllowStatus(a.Worktree, 1, "merge-tree", "--write-tree", "--name-only", "-z", a.CandidateSHA, target)
	if err != nil {
		return "", nil, err
	}
	tree, rest, ok := strings.Cut(out, "\x00")
	if !ok || !stewardOID.MatchString(tree) {
		return "", nil, errors.New("Git did not produce an identified merge tree")
	}
	var paths []string
	total := 0
	for path := range strings.SplitSeq(rest, "\x00") {
		if path == "" {
			break
		}
		if !safeRelative(path) || !conf.allowsFile(path) || len(paths) >= 32 {
			return "", nil, errors.New("conflict exceeds registered file scope")
		}
		entry, err := git(a.Worktree, "ls-tree", tree, "--", path)
		if err != nil || (!strings.HasPrefix(entry, "100644 blob ") && !strings.HasPrefix(entry, "100755 blob ")) {
			return "", nil, errors.New("conflict requires a regular text file")
		}
		raw, err := git(a.Worktree, "cat-file", "blob", tree+":"+path)
		total += len(raw)
		if err != nil || len(raw) > 64<<10 || total > 128<<10 || !utf8.ValidString(raw) || !mergeMarker.MatchString(raw) {
			return "", nil, errors.New("conflict requires manual resolution of a non-text or structural change")
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return "", nil, errors.New("GitHub and the local merge disagree about the conflict; refresh the PR")
	}
	commit, err := git(a.Worktree, "commit-tree", tree, "-p", a.CandidateSHA, "-p", target, "-m", "Prepare approved PR conflict resolution")
	if err != nil || !stewardOID.MatchString(commit) {
		return "", nil, errors.New("could not retain the local conflict tree")
	}
	return commit, paths, nil
}

func (a *Attempt) preparePlanTree() error {
	if a.Steward == nil || a.Steward.MergeCommit == "" {
		if err := a.prepare(); err != nil {
			return err
		}
		return a.cleanAt(a.BaseSHA)
	}
	if _, err := os.Lstat(a.Worktree); os.IsNotExist(err) {
		if err = a.prepare(); err != nil {
			return err
		}
	}
	if err := a.checkTree(); err != nil {
		return err
	}
	head, err := git(a.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head == a.BaseSHA {
		if err = a.cleanAt(head); err != nil {
			return err
		}
		if _, err = git(a.Worktree, "merge", "--ff-only", "--no-edit", a.Steward.MergeCommit); err != nil {
			return err
		}
	}
	if err = a.cleanAt(a.Steward.MergeCommit); err != nil {
		return err
	}
	a.CandidateSHA = a.Steward.MergeCommit
	return nil
}

func (a Attempt) resolvedStewardConflicts(head string) error {
	if a.Steward == nil || a.Steward.MergeCommit == "" {
		return nil
	}
	for _, path := range a.Steward.Conflicts {
		if _, err := os.Lstat(filepath.Join(a.Worktree, path)); os.IsNotExist(err) {
			return errors.New("conflicting file was removed; resolve its contents explicitly")
		}
		text, err := readFile(a.Worktree, path)
		if err != nil || mergeMarker.MatchString(text) {
			return errors.New("candidate contains unresolved merge markers")
		}
		old, err := git(a.Worktree, "rev-parse", a.Steward.MergeCommit+":"+path)
		if err != nil {
			return err
		}
		result, err := git(a.Worktree, "rev-parse", head+":"+path)
		if err != nil || old == result {
			return errors.New("candidate did not resolve every conflicting file")
		}
	}
	return nil
}
