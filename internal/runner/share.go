// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"strings"
	"unicode/utf8"
)

// DiffForSharing prepares only registered text files on an identified clean candidate.
func DiffForSharing(id string) (Attempt, string, error) {
	a, err := Get(id)
	if err != nil {
		return a, "", err
	}
	if a.CandidateSHA == "" || a.State != "completed" {
		return a, "", errors.New("a completed reviewed candidate is required")
	}
	if err = a.cleanAt(a.CandidateSHA); err != nil {
		return a, "", err
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=2", a.changeBase(), a.CandidateSHA, "--"}
	changed, err := changedPaths(a.Worktree, a.changeBase(), a.CandidateSHA)
	if err != nil || len(changed) == 0 {
		return a, "", errors.New("cannot identify candidate files")
	}
	for _, p := range changed {
		if !a.Project.Config.allowsFile(p) {
			return a, "", errors.New("unapproved path in candidate diff")
		}
	}
	args = append(args, changed...)
	raw, err := git(a.Worktree, args...)
	if err != nil {
		return a, "", err
	}
	if len(raw) == 0 || len(raw) > 24<<10 || !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
		return a, "", errors.New("diff must be nonempty UTF-8 text under 24 KiB")
	}
	return a, incidents.Redact(raw), nil
}
