// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

type IntegrationCheck struct {
	ValidationSandbox string   `json:"validationSandbox,omitempty"`
	ID                string   `json:"id"`
	Attempt           string   `json:"attemptId"`
	Workspace         string   `json:"workspace"`
	Repository        string   `json:"repository"`
	TargetRef         string   `json:"targetRef"`
	TargetSHA         string   `json:"targetSha"`
	CandidateSHA      string   `json:"candidateSha"`
	CombinedSHA       string   `json:"combinedSha,omitempty"`
	CriteriaVersion   string   `json:"criteriaVersion"`
	Policy            string   `json:"policy"`
	Worktree          string   `json:"worktree"`
	Status            string   `json:"status"`
	ExitCode          *int     `json:"exitCode,omitempty"`
	Command           []string `json:"command"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
	CleanedAt         string   `json:"cleanedAt,omitempty"`
	ArchiveRef        string   `json:"archiveRef,omitempty"`
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

type integrationOperation struct {
	Version    int                 `json:"version"`
	Operation  string              `json:"operation"`
	Home       string              `json:"home"`
	Project    string              `json:"project"`
	Repository string              `json:"repository"`
	TargetRef  string              `json:"targetRef"`
	Identity   integrationIdentity `json:"identity"`
}

type integrationIdentity struct {
	AttemptID    string `json:"attempt_id"`
	CheckID      string `json:"check_id"`
	CandidateSHA string `json:"candidate_sha"`
	TargetSHA    string `json:"target_sha"`
	CombinedSHA  string `json:"combined_sha"`
}

func (c IntegrationCheck) identity() integrationIdentity {
	return integrationIdentity{AttemptID: c.Attempt, CheckID: c.ID, CandidateSHA: c.CandidateSHA, TargetSHA: c.TargetSHA, CombinedSHA: c.CombinedSHA}
}

type integrationResult struct {
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	TargetSHA   string `json:"targetSha"`
	CombinedSHA string `json:"combinedSha"`
	ArchiveRef  string `json:"archiveRef"`
}

func integrationGit(ctx context.Context, op string, c IntegrationCheck, a Attempt) (integrationResult, error) {
	var result integrationResult
	dir, err := home()
	if err != nil {
		return result, err
	}
	if !idPattern.MatchString(c.ID) || c.Attempt != a.ID || a.Branch != "bugsnitch/"+a.ID ||
		c.Repository != a.Repository || c.Workspace != a.Workspace || c.Worktree != filepath.Join(dir, "integration", c.ID) {
		return result, errors.New("integration check does not match the recorded attempt or location")
	}
	request := integrationOperation{Version: 1, Operation: op, Home: dir, Project: a.Project.Path,
		Repository: c.Repository, TargetRef: c.TargetRef, Identity: c.identity()}
	err = nativeOperation(ctx, "--integration", request, &result)
	if err == nil && result.Status != "" {
		err = errors.New(result.Reason)
	}
	return result, err
}

func CheckCombined(ctx context.Context, id, ref string) (IntegrationCheck, error) {
	return checkCombined(ctx, id, ref, "")
}

func CheckCombinedAt(ctx context.Context, id, ref, expected string) (IntegrationCheck, error) {
	if expected == "" {
		return IntegrationCheck{}, errors.New("an unattended integration check needs an exact target revision")
	}
	return checkCombined(ctx, id, ref, expected)
}

// Retain the exact inspected base without moving the checkout or the user's
// tracking refs. The fetch, when needed, uses the same isolated Git client as publication.
func RetainInspectedBase(ctx context.Context, p Project, branch, expected string) (string, error) {
	decoded, err := hex.DecodeString(expected)
	if err != nil || (len(decoded) != 20 && len(decoded) != 32) || strings.ToLower(expected) != expected ||
		!github.ValidBranch(branch) || !repoPattern.MatchString(p.Repository) {
		return "", errors.New("invalid inspected integration target")
	}
	if err := checkOrigin(p.Path, p.Repository); err != nil {
		return "", err
	}
	ref := "refs/bugsnitch/inspections/" + expected
	if kind, err := git(p.Path, "cat-file", "-t", expected); err != nil || kind != "commit" {
		err = withRemoteGit(ctx, p.Path, "https", func(run remoteGit) error {
			if _, err := run("fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-maintenance", "--",
				"https://github.com/"+p.Repository+".git", "refs/heads/"+branch+":refs/heads/inspected-target"); err != nil {
				return errors.New("could not fetch the inspected integration target")
			}
			got, err := run("rev-parse", "--verify", "refs/heads/inspected-target^{commit}")
			if err != nil || got != expected {
				return errors.New("integration target moved since inspection")
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	if got, err := git(p.Path, "rev-parse", "--verify", ref+"^{commit}"); err == nil && got == expected {
		return ref, nil
	}
	if _, err := git(p.Path, "update-ref", ref, expected, strings.Repeat("0", len(expected))); err != nil {
		if got, e := git(p.Path, "rev-parse", "--verify", ref+"^{commit}"); e != nil || got != expected {
			return "", err
		}
	}
	return ref, nil
}

// A paired checkout may stay on an older commit after a preceding ticket merges.
// Pin the configured GitHub branch before worktree creation and admission.
func portalTicketBase(ctx context.Context, p Project, branch string) (string, error) {
	if !github.ValidBranch(branch) || strings.HasPrefix(branch, "bugsnitch/") || !repoPattern.MatchString(p.Repository) {
		return "", errors.New("invalid portal publication target")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var sha string
	err := withRemoteGit(ctx, p.Path, "https", func(run remoteGit) error {
		ref := "refs/heads/" + branch
		tip, err := run("ls-remote", "--refs", "--", "https://github.com/"+p.Repository+".git", ref)
		var actual string
		sha, actual, _ = strings.Cut(tip, "\t")
		if err != nil || actual != ref || !stewardOID.MatchString(sha) {
			return errors.New("could not resolve the remote portal publication target")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	_, err = RetainInspectedBase(ctx, p, branch, sha)
	return sha, err
}

func checkCombined(ctx context.Context, id, ref, expected string) (IntegrationCheck, error) {
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
	if err = verifyIssue(ctx, a.Issue); err != nil {
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
	c = IntegrationCheck{ID: checkID, Attempt: id, Workspace: a.Workspace, Repository: a.Repository,
		TargetRef: ref, CandidateSHA: a.CandidateSHA, CriteriaVersion: a.Issue.UpdatedAt, Policy: a.Project.Config.editPolicy(), ValidationSandbox: a.Project.Config.ValidationSandbox,
		Worktree: filepath.Join(dir, "integration", checkID), Status: "requested", Command: a.Project.Config.Validate, CreatedAt: now()}
	resolved, err := integrationGit(ctx, "resolve", c, a)
	if err != nil {
		return IntegrationCheck{}, err
	}
	c.TargetSHA = resolved.TargetSHA
	if expected != "" && c.TargetSHA != expected {
		return IntegrationCheck{}, errors.New("local target ref differs from the inspected revision")
	}
	if err = c.save(); err != nil {
		return c, err
	}
	finish := func(status string, e error) (IntegrationCheck, error) {
		c.Status = status
		return c, errors.Join(e, c.save())
	}
	combined, err := integrationGit(ctx, "combine", c, a)
	if err != nil {
		if combined.Status != "" {
			return finish(combined.Status, err)
		}
		return finish("worktree_unavailable", err)
	}
	c.CombinedSHA = combined.CombinedSHA
	c.Status = "validating"
	if err = c.save(); err != nil {
		return c, err
	}
	c.ExitCode, err = validateCombined(ctx, &a, &c)
	if errors.Is(err, errValidationUnconfirmed) {
		return finish("validation_unconfirmed", err)
	}
	if err != nil {
		return finish("validation_failed", err)
	}
	verified, err := integrationGit(ctx, "verify", c, a)
	if err != nil {
		if verified.Status != "" {
			return finish(verified.Status, err)
		}
		return finish("unverified", err)
	}
	if err = verifyIssue(ctx, a.Issue); err != nil {
		return finish("stale", err)
	}
	return finish("passed", nil)
}

// CleanupIntegration releases one completed, clean detached checkout while retaining
// its verification receipt and the underlying tested commit in the local repository.
func CleanupIntegration(ctx context.Context, attemptID, checkID string) error {
	if !idPattern.MatchString(checkID) {
		return errors.New("invalid integration check ID")
	}
	unlock, err := attemptLock(attemptID)
	if err != nil {
		return err
	}
	defer unlock()
	a, err := Get(attemptID)
	if err != nil {
		return err
	}
	data, err := query(integrationTable + "SELECT data FROM integration_checks WHERE id=" + literal(checkID) + " AND attempt_id=" + literal(attemptID) + ";")
	if err != nil {
		return err
	}
	var c IntegrationCheck
	if len(data) == 0 {
		return errors.New("integration check missing for this attempt")
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return err
	}
	if c.ID != checkID {
		return errors.New("integration check identity differs from its receipt")
	}
	switch c.Status {
	case "passed", "validation_failed", "unverified", "stale":
	default:
		return errors.New("cleanup requires a finished integration check with a recorded combined revision")
	}
	result, err := integrationGit(ctx, "cleanup", c, a)
	if err != nil {
		return err
	}
	if c.CleanedAt != "" && c.ArchiveRef == result.ArchiveRef {
		return nil
	}
	c.CleanedAt = now()
	c.ArchiveRef = result.ArchiveRef
	return c.save()
}
