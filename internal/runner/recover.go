// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func attemptLock(id string) (func(), error) {
	if !idPattern.MatchString(id) {
		return nil, errors.New("invalid attempt ID")
	}
	dir, err := home()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "attempt-"+id+".lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, errors.New("attempt is already owned by a running process or local lock unavailable")
	}
	return func() { f.Close() }, nil
}

// RecoverCommit recognizes exactly the commit described by the pre-write
// manifest. A partial edit, a moved branch, or a still-held lock demands inspection.
func RecoverCommit(id string) (Attempt, error) {
	var a Attempt
	unlock, err := attemptLock(id)
	if err != nil {
		return a, err
	}
	defer unlock()
	a, err = Get(id)
	if err != nil {
		return a, err
	}
	if a.State == "waiting" && a.Phase == "validate" && a.Intent == nil {
		return a, nil
	}
	if a.State != "running" || a.Phase != "implement" || a.Intent == nil {
		return a, errors.New("no interrupted candidate commit ready for reconciliation")
	}
	if err = a.checkTree(); err != nil {
		return a, err
	}
	head, err := git(a.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return a, err
	}
	if err = a.cleanAt(head); err != nil {
		return a, err
	}
	parents, err := git(a.Worktree, "rev-list", "--parents", "-n", "1", head)
	if err != nil || parents != head+" "+a.Intent.Parent {
		return a, errors.New("candidate parent differs from the pre-commit record")
	}
	changed, err := changedPaths(a.Worktree, a.Intent.Parent, head)
	if err != nil {
		return a, err
	}
	if len(changed) != len(a.Intent.SHA256) {
		return a, errors.New("candidate file set differs from the pre-commit record")
	}
	for _, p := range changed {
		if !slices.Contains(a.Project.Config.Files, p) {
			return a, errors.New("candidate changed an unapproved file")
		}
		data, err := readFile(a.Worktree, p)
		if err != nil {
			return a, err
		}
		if fmt.Sprintf("%x", sha256.Sum256([]byte(data))) != a.Intent.SHA256[p] {
			return a, errors.New("candidate content differs from the pre-commit record")
		}
		// The bounded worker preserves the base file mode. Reject unrelated metadata changes.
		baseEntry, err := git(a.Worktree, "ls-tree", a.Intent.Parent, "--", p)
		if err != nil {
			return a, err
		}
		candidateEntry, err := git(a.Worktree, "ls-tree", head, "--", p)
		if err != nil {
			return a, err
		}
		baseFields, candidateFields := strings.Fields(baseEntry), strings.Fields(candidateEntry)
		if len(baseFields) == 0 || len(candidateFields) == 0 || baseFields[0] != candidateFields[0] {
			return a, errors.New("candidate file mode differs from base")
		}
	}
	a.CandidateSHA = head
	artifact := a.Intent.Artifact
	artifact.ChangedFiles = changed
	a.add(artifact)
	a.Intent = nil
	a.Phase = "validate"
	a.State = "waiting"
	a.Message = "Recovered the recorded candidate commit. Continue locally to validate it."
	if _, err = settle(&a); err != nil {
		return a, err
	}
	if err = hook(AfterFinish, &a); err != nil {
		return a, err
	}
	return a, nil
}
func Cleanup(id string) error {
	unlock, err := attemptLock(id)
	if err != nil {
		return err
	}
	defer unlock()
	a, err := Get(id)
	if err != nil {
		return err
	}
	if a.State != "completed" && a.State != "cancelled" {
		return errors.New("cleanup requires a completed or cancelled attempt")
	}
	head := a.CandidateSHA
	if head == "" {
		head = a.BaseSHA
	}
	return a.worktreeOp("cleanup", head)
}

// ReplayEnded permits the sync process to replay an acknowledgment only while
// the original CLI is absent and the same quiesced revision remains current.
func ReplayEnded(id string, version int, replay func(string) error) error {
	unlock, err := attemptLock(id)
	if err != nil {
		return nil
	}
	defer unlock()
	a, err := Get(id)
	if err != nil {
		return err
	}
	if a.Version != version || a.State == "running" {
		return nil
	}
	return replay(id)
}
