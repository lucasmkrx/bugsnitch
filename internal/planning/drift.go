// SPDX-License-Identifier: MPL-2.0
package planning

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

var ErrIssueDrift = errors.New("GitHub issue changed, is closed, or lacks a recent authoritative read; review a fresh plan")

func requirements(i github.Issue) string { return i.RequirementsDigest() }
func issueTitle(t string) string {
	s := []rune(incidents.Redact(t))
	if len(s) > 240 {
		s = s[:240]
	}
	return string(s)
}

// Only export an exact, bounded canonical revision. A redacted title is useful
// for display but cannot pass the runner's exact-title identity check.
func selectionRevision(i github.Issue) string {
	if _, err := time.Parse(time.RFC3339, i.UpdatedAt); err != nil || len(i.UpdatedAt) > 40 || issueTitle(i.Title) != i.Title || strings.TrimSpace(i.Title) == "" {
		return ""
	}
	return i.UpdatedAt
}

// BindIssues seals the observed issue generation into the proposed plan before the owner approves it.
func BindIssues(ctx context.Context, tx *sql.Tx, ws, p, plan string, doc domain.Plan) error {
	for _, t := range doc.Tickets {
		_, err := tx.ExecContext(ctx, `INSERT INTO plan_issue_bindings(workspace_id,project_id,plan_id,number,generation)
		SELECT z.workspace_id,z.project_id,$3,z.number,z.generation FROM planned_issue_state z JOIN github_mappings m USING(workspace_id,project_id,installation_id,repository_id)
	 WHERE z.workspace_id=$1 AND z.project_id=$2 AND z.number=$4 AND m.active AND z.availability='current' AND z.checked_at>now()-interval '20 minutes' FOR SHARE OF z,m`, ws, p, plan, t.Number)
		if err != nil {
			return err
		}
	}
	return nil
}

// CheckIssue is a no-op for tickets outside the publication system. All published work
// requires a current plan-specific binding, even when the project normally allows ad hoc work.
func CheckIssue(ctx context.Context, tx *sql.Tx, ws, p, plan string, number int, allowClosed bool) error {
	var good bool
	err := tx.QueryRowContext(ctx, `SELECT m.active AND z.availability='current' AND (z.source_state='open' OR $5 AND z.source_state='closed') AND z.checked_at>now()-interval '20 minutes'
	AND EXISTS(SELECT FROM plan_issue_bindings b WHERE b.plan_id=$3 AND (b.workspace_id,b.project_id,b.number,b.generation)=(z.workspace_id,z.project_id,z.number,z.generation))
	FROM planned_issue_state z JOIN github_mappings m USING(workspace_id,project_id,installation_id,repository_id)
	WHERE z.workspace_id=$1 AND z.project_id=$2 AND z.number=$4 FOR SHARE OF z,m`, ws, p, plan, number, allowClosed).Scan(&good)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !good {
		return ErrIssueDrift
	}
	return nil
}

type readCanonical func(context.Context, int64, int64, string, int64) (github.Issue, error)

func (s Store) SyncDrift(ctx context.Context, app *github.App, claim storage.JobClaim) error {
	return s.reconcileClaimed(audit.WithActor(ctx, audit.Service, "plan-publisher"), app.ReadIssue, claim)
}

func (s Store) reconcileIssue(ctx context.Context, read readCanonical) error {
	claim, err := storage.ClaimJob(ctx, s.DB, "drift")
	if err != nil {
		return err
	}
	return s.reconcileClaimed(ctx, read, claim)
}

func (s Store) reconcileClaimed(ctx context.Context, read readCanonical, claim storage.JobClaim) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ws, p, name, digest, source, avail string
	var n, identity, install, repository int64
	var failures int
	err = tx.QueryRowContext(ctx, `SELECT z.workspace_id,z.project_id,p.repository,z.number,z.github_id,z.installation_id,z.repository_id,z.digest,z.source_state,z.availability,z.failures FROM planned_issue_state z
	JOIN projects p ON (p.workspace_id,p.id)=(z.workspace_id,z.project_id) JOIN github_mappings m USING(installation_id,repository_id)
	WHERE m.project_id=z.project_id AND m.workspace_id=z.workspace_id AND m.active AND (z.workspace_id,z.project_id,z.number)=($1,$2,$3)
	AND z.next_check=$4::timestamptz AND z.next_check>clock_timestamp() FOR UPDATE OF z SKIP LOCKED FOR SHARE OF m SKIP LOCKED`,
		claim.Workspace, claim.Project, claim.Number, claim.Fence).Scan(&ws, &p, &name, &n, &identity, &install, &repository, &digest, &source, &avail, &failures)
	if err != nil {
		return err
	}
	i, readErr := read(ctx, install, repository, name, n)
	if readErr != nil || i.ID != identity || i.Number != n || len(i.PullRequest) > 0 || (i.State != "open" && i.State != "closed") {
		reason := "GitHub read unavailable"
		invalidate := false
		var api *github.APIError
		if readErr == nil || errors.Is(readErr, github.ErrSubjectChanged) {
			reason = "Canonical issue identity changed"
			invalidate = true
		} else if errors.As(readErr, &api) {
			switch api.Status {
			case 301, 302, 307, 308, 404, 410:
				reason = "Issue missing, moved, or access revoked"
				invalidate = true
			case 401, 403:
				reason = "Repository authorization or rate limit blocked the read"
			}
		}
		delay := time.Minute << min(failures, 6)
		if api != nil && api.RetryAfter > delay {
			delay = api.RetryAfter
		}
		_, err = tx.ExecContext(ctx, `UPDATE planned_issue_state SET availability='unknown',reason=$4,generation=generation+CASE WHEN $5 AND reason<>$4 THEN 1 ELSE 0 END,
		failures=least(8,failures+1),next_check=now()+($6::int*interval '1 second') WHERE workspace_id=$1 AND project_id=$2 AND number=$3`, ws, p, n, reason, invalidate, int(delay.Seconds()))
	} else {
		newDigest := requirements(i)

		changed := digest != "" && (digest != newDigest || source != i.State)
		_, err = tx.ExecContext(ctx, `UPDATE planned_issue_state SET availability='current',reason='',generation=generation+CASE WHEN $4 THEN 1 ELSE 0 END,
		title=$5,digest=$6,source_state=$7,selection_revision=$8,checked_at=now(),next_check=now()+interval '5 minutes',failures=0 WHERE workspace_id=$1 AND project_id=$2 AND number=$3`, ws, p, n, changed, issueTitle(i.Title), newDigest, strings.ToLower(i.State), selectionRevision(i))
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
