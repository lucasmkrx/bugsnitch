// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

var ErrUnattendedBudget = errors.New("unattended model-turn budget exhausted")

type unattendedPass struct {
	grantID        string
	firstAttemptID string
	publishTarget  string
	projectDigest  string
	corrections    bool
	takeTurn       func(context.Context) error
}

type unattendedKey struct{}

func projectDigest(p Project) string {
	b, _ := json.Marshal(p)
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func UnattendedProject(workspace, repo, mappedPath string) (string, error) {
	return supervisorProject(workspace, repo, mappedPath, false)
}

// PortalProject uses the same model adapters as an interactive worker on this checkout.
func PortalProject(workspace, repo, mappedPath string) (string, error) {
	return supervisorProject(workspace, repo, mappedPath, true)
}

func supervisorProject(workspace, repo, mappedPath string, portal bool) (string, error) {
	p, err := project(workspace, repo)
	if err != nil {
		return "", err
	}
	if mappedPath != p.Path {
		return "", errors.New("the paired repository mapping differs from the worker checkout")
	}
	if err = checkOrigin(p.Path, p.Repository); err != nil {
		return "", err
	}
	for _, phase := range []string{"plan", "implement", "review"} {
		provider := p.Config.forStage(phase).Provider
		if portal && provider == "claude" && strings.TrimSpace(os.Getenv("BUGSNITCH_ANTHROPIC_API_KEY")) == "" {
			return "", errors.New("provide BUGSNITCH_ANTHROPIC_API_KEY for the configured Claude worker stage")
		}
		if !portal && provider != "claude" {
			return "", errors.New("unattended execution requires the Claude API provider for plan, implement and review")
		}
	}
	return projectDigest(p), nil
}

// LockUnattended keeps a project's supervisor single-owner across processes.
// The operating system releases the lock if the supervisor exits.
func LockUnattended(projectID string) (func(), error) {
	dir, err := home()
	if err != nil {
		return nil, err
	}
	id := sha256.Sum256([]byte(projectID))
	f, err := os.OpenFile(filepath.Join(dir, "unattended-"+hex.EncodeToString(id[:])+".lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, errors.New("another unattended supervisor owns this project or its lock is unavailable")
	}
	return func() { f.Close() }, nil
}

type UnattendedAuthority struct {
	GrantID        string
	FirstAttemptID string
	PublishTarget  string
	Digest         string
	Corrections    bool
	TakeTurn       func(context.Context) error
}

func (g UnattendedAuthority) bind(ctx context.Context) context.Context {
	return context.WithValue(ctx, unattendedKey{}, unattendedPass{grantID: g.GrantID, firstAttemptID: g.FirstAttemptID, publishTarget: g.PublishTarget, projectDigest: g.Digest, corrections: g.Corrections, takeTurn: g.TakeTurn})
}

func StartUnattendedPass(ctx context.Context, workspace string, issue Issue, scope protocol.RunScope, grant UnattendedAuthority, admitted func(Summary)) (Attempt, error) {
	ctx = grant.bind(ctx)
	return StartPass(ctx, workspace, issue, scope, admitted)
}

// ContinueUnattendedPass consumes an exact checkpoint. A concurrent local
// Continue must win the same versioned journal claim before it can execute.
func ContinueUnattendedPass(ctx context.Context, a Attempt, grant UnattendedAuthority, reason string) (Attempt, error) {
	if a.UnattendedGrant != grant.GrantID || grant.GrantID == "" || a.OwnerQuestion != "" ||
		a.ReasonCode != reason || (reason != protocol.PlanReviewRequired && reason != protocol.RecoveredCandidate) {
		return a, errors.New("unattended continuation no longer matches the authorized checkpoint")
	}
	return ContinueAt(grant.bind(ctx), a.ID, "", a.Version)
}

func AnswerUnattendedQuestion(ctx context.Context, a Attempt, grant UnattendedAuthority, feedback string) (Attempt, error) {
	if grant.GrantID == "" || a.UnattendedGrant != grant.GrantID || a.State != "waiting" ||
		a.ReasonCode != protocol.OwnerInputRequired || a.OwnerQuestion == "" || feedback == "" || len(feedback) > 2048 {
		return a, ErrAuthorityStale
	}
	return ContinueAt(grant.bind(ctx), a.ID, feedback, a.Version)
}

func StewardUnattended(ctx context.Context, a Attempt, grant UnattendedAuthority, pull int) (Attempt, error) {
	if a.UnattendedGrant != grant.GrantID || grant.GrantID == "" || projectDigest(a.Project) != grant.Digest {
		return a, errors.New("PR correction differs from the unattended grant")
	}
	return Steward(grant.bind(ctx), a.ID, a.Version, a.CandidateSHA, pull)
}

// The lock establishes that the local execution owner is gone. Its absence
// does not prove that an interrupted subprocess or provider call completed.
func UnattendedNeedsInspection(a Attempt) bool {
	unlock, err := attemptLock(a.ID)
	if err != nil {
		return false
	}
	defer unlock()
	fresh, err := Get(a.ID)
	return err == nil && fresh.Version == a.Version && fresh.State == "running"
}
