// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type IntegrationCheck struct {
	ID              string   `json:"id"`
	Attempt         string   `json:"attemptId"`
	Workspace       string   `json:"workspace"`
	Repository      string   `json:"repository"`
	TargetRef       string   `json:"targetRef"`
	TargetSHA       string   `json:"targetSha"`
	CandidateSHA    string   `json:"candidateSha"`
	CombinedSHA     string   `json:"combinedSha,omitempty"`
	CriteriaVersion string   `json:"criteriaVersion"`
	Policy          string   `json:"policy"`
	Worktree        string   `json:"worktree"`
	Status          string   `json:"status"`
	ExitCode        *int     `json:"exitCode,omitempty"`
	Command         []string `json:"command"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
}

const integrationTable = "CREATE TABLE IF NOT EXISTS integration_checks(id TEXT PRIMARY KEY,attempt_id TEXT NOT NULL REFERENCES journal_attempts(id),data TEXT NOT NULL); "

func (c *IntegrationCheck) save() error {
	c.UpdatedAt = now()
	_, err := query(integrationTable + "INSERT INTO integration_checks VALUES(" + literal(c.ID) + "," + literal(c.Attempt) + "," + packed(c) + ") ON CONFLICT(id) DO UPDATE SET data=excluded.data;")
	return err
}
func Checks(id string) ([]IntegrationCheck, error) {
	if _, err := Get(id); err != nil {
		return nil, err
	}
	b, err := query(integrationTable + "SELECT json_group_array(json(data)) FROM (SELECT data FROM integration_checks WHERE attempt_id=" + literal(id) + " ORDER BY rowid);")
	if err != nil {
		return nil, err
	}
	var checks []IntegrationCheck
	err = json.Unmarshal(b, &checks)
	return checks, err
}
func CheckCombined(ctx context.Context, id, ref string) (IntegrationCheck, error) {
	var c IntegrationCheck
	unlock, err := attemptLock(id)
	if err != nil {
		return c, err
	}
	defer unlock()
	a, err := Get(id)
	if err != nil {
		return c, err
	}
	if a.State != "completed" || a.CandidateSHA == "" {
		return c, errors.New("integration check requires a completed local candidate")
	}
	var reviewed bool
	for _, item := range a.Artifacts {
		if item.Phase != "review" || item.CandidateSHA != a.CandidateSHA || item.IssueVersion != a.Issue.UpdatedAt {
			continue
		}
		reviewed = true
		for _, f := range item.Findings {
			if f.Severity == "important" {
				reviewed = false
			}
		}
	}
	if !reviewed {
		return c, errors.New("candidate lacks current independent review")
	}
	if err = checkOrigin(a.Project.Path, a.Repository); err != nil {
		return c, err
	}
	if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/remotes/origin/") {
		return c, errors.New("use an explicit local branch or origin remote-tracking reference")
	}
	if _, err = git(a.Project.Path, "check-ref-format", ref); err != nil {
		return c, errors.New("invalid integration target")
	}
	if err = verifyIssue(ctx, a.Issue); err != nil {
		return c, err
	}
	candidate, err := git(a.Project.Path, "rev-parse", "--verify", "refs/heads/"+a.Branch+"^{commit}")
	if err != nil || candidate != a.CandidateSHA {
		return c, errors.New("candidate branch changed; review evidence is stale")
	}
	target, err := git(a.Project.Path, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return c, err
	}
	checkID, err := newID()
	if err != nil {
		return c, err
	}
	dir, err := home()
	if err != nil {
		return c, err
	}
	parent := filepath.Join(dir, "integration")
	if err = os.MkdirAll(parent, 0700); err != nil {
		return c, err
	}
	real, err := canonical(parent)
	if err != nil || real != parent {
		return c, errors.New("integration worktree parent moved")
	}
	c = IntegrationCheck{ID: checkID, Attempt: id, Workspace: a.Workspace, Repository: a.Repository, TargetRef: ref, TargetSHA: target, CandidateSHA: candidate, CriteriaVersion: a.Issue.UpdatedAt, Policy: "local-reviewed-files/v1", Worktree: filepath.Join(parent, checkID), Status: "requested", Command: a.Project.Config.Validate, CreatedAt: now()}
	if err = c.save(); err != nil {
		return c, err
	}
	finish := func(status string, e error) (IntegrationCheck, error) {
		c.Status = status
		return c, errors.Join(e, c.save())
	}
	if _, err = git(a.Project.Path, "worktree", "add", "--detach", "--", c.Worktree, target); err != nil {
		return finish("worktree_unavailable", err)
	}
	top, err := git(c.Worktree, "rev-parse", "--show-toplevel")
	if err != nil || top != c.Worktree {
		return finish("worktree_unavailable", errors.New("integration worktree identity changed"))
	}
	if _, err = git(c.Worktree, "merge", "--no-ff", "--no-edit", candidate); err != nil {
		return finish("merge_failed", errors.New("local integration failed; inspect retained worktree for conflicts"))
	}
	c.CombinedSHA, err = git(c.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return finish("unverified", err)
	}
	c.Status = "validating"
	if err = c.save(); err != nil {
		return c, err
	}
	local := a
	local.Worktree = c.Worktree
	c.ExitCode, err = validate(ctx, &local)
	if err != nil {
		return finish("validation_failed", err)
	}
	head, err := git(c.Worktree, "rev-parse", "HEAD")
	if err != nil || head != c.CombinedSHA {
		return finish("stale", errors.New("validation moved the combined candidate"))
	}
	dirty, err := git(c.Worktree, "status", "--porcelain", "--untracked-files=all")
	if err != nil || dirty != "" {
		return finish("unverified", errors.New("validation changed the integration checkout"))
	}
	for refName, sha := range map[string]string{ref: target, "refs/heads/" + a.Branch: candidate} {
		value, e := git(a.Project.Path, "rev-parse", "--verify", refName+"^{commit}")
		if e != nil || value != sha {
			return finish("stale", fmt.Errorf("reference changed during validation: %s", refName))
		}
	}
	if err = verifyIssue(ctx, a.Issue); err != nil {
		return finish("stale", err)
	}
	return finish("passed", nil)
}
