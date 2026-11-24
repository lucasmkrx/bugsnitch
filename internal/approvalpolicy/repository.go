// SPDX-License-Identifier: MPL-2.0
package approvalpolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

type limitedOutput struct {
	data  bytes.Buffer
	limit int
}

func (o *limitedOutput) Write(b []byte) (int, error) {
	if o.data.Len()+len(b) > o.limit {
		return 0, errors.New("Git result exceeds the policy evaluation limit")
	}
	return o.data.Write(b)
}

func git(ctx context.Context, repo string, limit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs", "--no-replace-objects", "-C", repo}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=", "LC_ALL=C"}
	var out limitedOutput
	out.limit = limit
	cmd.Stdout = &out
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("could not read %s from local Git objects", args[0])
	}
	return out.data.Bytes(), nil
}

// Inspect loads the policy at the exact target commit. A queued caller supplies
// the tested group's target/base and combined SHA with its authenticated data.
func Inspect(ctx context.Context, repo, base, head string, evidence Evidence) Decision {
	return inspect(ctx, repo, base, head, base, evidence)
}

func inspect(ctx context.Context, repo, base, head, policySHA string, e Evidence) Decision {
	out := Decision{Outcome: "hold", Base: base, Head: head, PolicyRevision: policySHA}
	hold := func(s string) Decision { out.Reasons = []string{s}; return out }
	if !revision.MatchString(base) || !revision.MatchString(head) || !revision.MatchString(policySHA) {
		return hold("use full immutable 40-character commit SHAs")
	}
	checked := map[string]bool{}
	for _, sha := range []string{base, head, policySHA} {
		if checked[sha] {
			continue
		}
		kind, err := git(ctx, repo, 16, "cat-file", "-t", sha)
		if err != nil || string(kind) != "commit\n" {
			return hold("a referenced commit is missing or has an unsupported object type")
		}
		checked[sha] = true
	}
	// Triple-dot preserves the PR's authored change set even when the target
	// has advanced. A diverged target still requires fresh combined evidence.
	names, err := git(ctx, repo, 256<<10, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", base+"..."+head, "--")
	if err != nil {
		return hold(err.Error())
	}
	var paths []string
	if len(names) > 0 {
		paths = strings.Split(strings.TrimSuffix(string(names), "\x00"), "\x00")
	}
	out.ChangedPaths = paths
	if slices.Contains(paths, File) {
		policySHA = base
		out.PolicyRevision = base
	}
	entry, err := git(ctx, repo, 1024, "ls-tree", policySHA, "--", File)
	if err != nil || (!bytes.HasPrefix(entry, []byte("100644 blob ")) && !bytes.HasPrefix(entry, []byte("100755 blob "))) {
		return hold("trusted revision lacks a regular approval policy file")
	}
	policy, err := git(ctx, repo, 64<<10, "cat-file", "blob", policySHA+":"+File)
	if err != nil {
		return hold(err.Error())
	}
	out = Evaluate(policy, policySHA, base, head, paths, e)
	if _, err = git(ctx, repo, 1, "merge-base", "--is-ancestor", base, head); err != nil {
		out.Outcome = "hold"
		out.Reasons = append(out.Reasons, "candidate requires combined validation against the current target")
	}
	return out
}

type HistoricalCase struct {
	ID             string   `json:"id"`
	Cohort         string   `json:"cohort"`
	Label          string   `json:"label"`
	LabelReference string   `json:"labelReference"`
	Base           string   `json:"base"`
	Head           string   `json:"head"`
	Evidence       Evidence `json:"evidence,omitempty"`
}

type CaseFile struct {
	Schema int              `json:"schema"`
	Cases  []HistoricalCase `json:"cases"`
}

type CaseResult struct {
	ID             string   `json:"id"`
	Cohort         string   `json:"cohort"`
	Label          string   `json:"label"`
	LabelReference string   `json:"labelReference"`
	Decision       Decision `json:"decision"`
}

type Counts struct {
	Cases      int `json:"cases"`
	WouldAllow int `json:"wouldAllow"`
	Hold       int `json:"hold"`
}

type Preview struct {
	Cases    []CaseResult                 `json:"cases"`
	ByCohort map[string]map[string]Counts `json:"byCohort"`
}

// Replay simulates policy changes without mutating Git or emitting a forge check.
func Replay(ctx context.Context, repo, policySHA string, input CaseFile) (Preview, error) {
	var out Preview
	if input.Schema != 1 || len(input.Cases) == 0 || len(input.Cases) > 1000 || (policySHA != "" && !revision.MatchString(policySHA)) {
		return out, errors.New("use schema 1 with 1–1000 labeled cases and an optional immutable policy SHA")
	}
	seen := map[string]bool{}
	seenChanges := map[string]bool{}
	for _, item := range input.Cases {
		if !short(item.ID) || seen[item.ID] || !short(item.LabelReference) || (item.Cohort != "calibration" && item.Cohort != "holdout") ||
			(item.Label != "clean" && item.Label != "regression" && item.Label != "revert") {
			return out, errors.New("each case needs a unique ID, calibration or holdout cohort, outcome label and reference")
		}
		pair := item.Base + "..." + item.Head
		if seenChanges[pair] {
			return out, errors.New("a change may appear only once across both cohorts")
		}
		seenChanges[pair] = true
		seen[item.ID] = true
	}
	out.ByCohort = map[string]map[string]Counts{"holdout": {}, "calibration": {}}
	for _, item := range input.Cases {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		source := policySHA
		if source == "" {
			source = item.Base
		}
		result := inspect(ctx, repo, item.Base, item.Head, source, item.Evidence)
		out.Cases = append(out.Cases, CaseResult{item.ID, item.Cohort, item.Label, item.LabelReference, result})
		count := out.ByCohort[item.Cohort][item.Label]
		count.Cases++
		if result.Outcome == "would_allow" {
			count.WouldAllow++
		} else {
			count.Hold++
		}
		out.ByCohort[item.Cohort][item.Label] = count
	}
	return out, nil
}
