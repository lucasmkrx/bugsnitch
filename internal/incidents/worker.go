package incidents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

// Run drains sanitized events independently of GitHub availability. Each external call has a deadline.
func (s Store) Run(ctx context.Context, app *github.App) {
	ctx = audit.WithActor(ctx, audit.Service, "incident-worker")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for i := 0; i < 100; i++ {
			err := s.processOne(ctx)
			if err != nil {
				if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
					slog.Warn("incident inbox unavailable")
				}
				break
			}
		}
		if app != nil {
			work, cancel := context.WithTimeout(ctx, 45*time.Second)
			if err := s.autoPromote(work); err != nil && work.Err() == nil {
				slog.Warn("automatic triage deferred")
			}
			err := s.syncOne(work, app)
			cancel()
			if err != nil && !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
				slog.Warn("incident issue synchronization deferred")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type intent struct {
	workspace, project, id, repo, marker, status, observed, expected, steps, source string
	installation, repository, number, issueID                                       int64
	tries                                                                           int
	approved                                                                        time.Time
}

func quoteInput(s string) string {
	clean := Redact(s)
	if clean == "" {
		return "Unknown"
	}
	// Keep untrusted report text in a code block, including Markdown and mentions.
	return "    " + strings.ReplaceAll(clean, "\n", "\n    ")
}
func issueText(j intent) (string, string) {
	title := "Investigate reported application behavior"
	if j.source == "sentry" {
		title = "Investigate application error reported by Sentry"
	}
	return title, fmt.Sprintf("## Observed behavior\n\n%s\n\n## Expected behavior\n\n%s\n\n## Reproduction steps reported\n\n%s\n\nIndependent reproduction and impact assessment are pending.\n\nBugsnitch incident %s\n\n%s", quoteInput(j.observed), quoteInput(j.expected), quoteInput(j.steps), j.id, marker(j.marker))
}
func marker(s string) string { return "<!-- bugsnitch:incident:" + s + " -->" }

func (s Store) syncOne(ctx context.Context, app *github.App) error {
	claim, err := storage.ClaimJob(ctx, s.DB, "incident_issue")
	if err != nil {
		return err
	}
	return s.syncClaimed(ctx, app, claim)
}

func (s Store) syncClaimed(ctx context.Context, app *github.App, claim storage.JobClaim) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var j intent
	err = tx.QueryRowContext(ctx, `SELECT x.workspace_id,x.project_id,x.incident_id,p.repository,x.marker,x.status,x.installation_id,x.repository_id,coalesce(x.number,0),coalesce(x.issue_id,0),x.attempt_count,x.approved_at,i.observed,i.expected,i.steps,i.source
	FROM incident_issues x JOIN github_mappings m USING(workspace_id,project_id,installation_id,repository_id)
	JOIN projects p ON (p.workspace_id,p.id)=(x.workspace_id,x.project_id) JOIN incidents i ON i.id=x.incident_id
	WHERE m.active AND (x.status<>'queued' OR (NOT p.paused AND p.allow_promotion AND x.policy_version=p.policy_version)) AND x.status<>'blocked' AND (x.workspace_id,x.project_id,x.incident_id)=($1,$2,$3)
	AND x.next_run=$4::timestamptz AND x.next_run>clock_timestamp() FOR UPDATE OF x SKIP LOCKED`,
		claim.Workspace, claim.Project, claim.Item, claim.Fence).Scan(
		&j.workspace, &j.project, &j.id, &j.repo, &j.marker, &j.status, &j.installation, &j.repository, &j.number, &j.issueID, &j.tries, &j.approved, &j.observed, &j.expected, &j.steps, &j.source)
	if err != nil {
		return err
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT active FROM github_mappings WHERE workspace_id=$1 AND project_id=$2 AND repository_id=$3 AND installation_id=$4 FOR SHARE`, j.workspace, j.project, j.repository, j.installation).Scan(&active); err != nil {
		return err
	}
	if !active {
		return nil
	}
	repo, err := app.Repository(ctx, j.installation, j.repository, j.repo)
	if err != nil {
		return reschedule(ctx, tx, j, j.status, "GitHub authorization or repository access unavailable", err)
	}
	switch j.status {
	case "queued":
		// Commit uncertainty BEFORE HTTP. Every observer after this commit may only search.
		_, err = tx.ExecContext(ctx, `UPDATE incident_issues SET status='reconciling',next_run=now()+interval '2 minutes',reason='Confirming issue creation' WHERE incident_id=$1`, j.id)
		if err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return s.sendIssue(ctx, j, repo)
	case "reconciling":
		candidate, readErr := repo.Find(ctx, marker(j.marker), j.approved)
		if readErr != nil || candidate == nil {
			return reschedule(ctx, tx, j, "reconciling", "Uncertain write. Waiting for an issue created by this app with the recorded marker", readErr)
		}
		return recordIssue(ctx, tx, j, *candidate)
	case "linked":
		i, readErr := repo.Get(ctx, j.number)
		if readErr != nil {
			return reschedule(ctx, tx, j, "linked", "Current GitHub issue state unavailable", readErr)
		}
		if i.ID != j.issueID {
			return reschedule(ctx, tx, j, "blocked", "Issue identity changed. Owner reconciliation required", nil)
		}
		return recordIssue(ctx, tx, j, i)
	}
	return ErrInvalid
}

func (s Store) sendIssue(ctx context.Context, j intent, repo *github.Repository) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize the final write against a concurrent removal, permission change or reconcile.
	var permitted bool
	err = tx.QueryRowContext(ctx, `SELECT m.active AND x.status='reconciling' AND x.number IS NULL AND p.repository=$5 AND p.allow_promotion AND NOT p.paused AND x.policy_version=p.policy_version
	FROM incident_issues x JOIN github_mappings m USING(workspace_id,project_id,installation_id,repository_id)
	JOIN projects p ON (p.workspace_id,p.id)=(x.workspace_id,x.project_id)
	WHERE x.incident_id=$1 AND x.marker=$2 AND x.repository_id=$3 AND x.installation_id=$4 FOR UPDATE OF x FOR SHARE OF m,p`, j.id, j.marker, j.repository, j.installation, j.repo).Scan(&permitted)
	if err != nil {
		return err
	}
	if !permitted {
		return ErrUnmapped
	}
	title, body := issueText(j)
	issue, err := repo.Create(ctx, title, body)
	if err != nil {
		var failure *github.APIError
		if errors.As(err, &failure) {
			switch failure.Status {
			case 401, 403, 429:
				return reschedule(ctx, tx, j, "queued", "GitHub denied or rate limited issue creation", err)
			case 404, 410, 422:
				return reschedule(ctx, tx, j, "blocked", "GitHub rejected issue creation. Check installation access and repository settings", err)
			}
		}
		return reschedule(ctx, tx, j, "reconciling", "Issue creation outcome uncertain", err)
	}
	return recordIssue(ctx, tx, j, issue)
}
func recordIssue(ctx context.Context, tx *sql.Tx, j intent, i github.Issue) error {
	if i.Number <= 0 || i.ID <= 0 || (i.State != "open" && i.State != "closed") {
		return ErrInvalid
	}
	hash := digest(i.Title + "\x00" + i.Body)
	var changed bool
	if err := tx.QueryRowContext(ctx, `SELECT (issue_id,github_state,requirements_digest) IS DISTINCT FROM ($2::bigint,$3::text,$4::text) FROM incident_issues WHERE incident_id=$1`, j.id, i.ID, i.State, hash).Scan(&changed); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE incident_issues SET status='linked',number=$2,issue_id=$3,github_state=$4,requirements_digest=$5,checked_at=now(),next_run=now()+interval '15 minutes',attempt_count=0,reason='' WHERE incident_id=$1`, j.id, i.Number, i.ID, i.State, hash)
	if err != nil {
		return err
	}
	if changed {
		_, err = tx.ExecContext(ctx, `UPDATE incidents SET version=version+1 WHERE id=$1`, j.id)
		if err != nil {
			return err
		}
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "GitHub issue identity, state or requirements changed", EvidenceRef: fmt.Sprintf("github-issue/%d", i.ID)})
		if err = event(ctx, tx, j.workspace, j.project, "incident.issue_synchronized", j.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func reschedule(ctx context.Context, tx *sql.Tx, j intent, status, reason string, problem error) error {
	backoff := time.Minute << min(j.tries, 6)
	var api *github.APIError
	if errors.As(problem, &api) && api.RetryAfter > backoff {
		backoff = api.RetryAfter
	}
	if j.tries >= 7 && status != "linked" {
		status = "blocked"
		reason += ". Retry limit reached. Review the outbox before resuming"
	}
	if j.tries >= 7 && status == "linked" {
		backoff = 24 * time.Hour
	}
	_, err := tx.ExecContext(ctx, `UPDATE incident_issues SET status=$2,reason=$3,next_run=now()+($4::int * interval '1 second'),attempt_count=least(8,attempt_count+1),github_state=CASE WHEN status='linked' THEN NULL ELSE github_state END WHERE incident_id=$1`, j.id, status, reason, int(backoff.Seconds()))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Auto promotion requires project opt-in and three independently identified recent errors on the same build/environment.
func (s Store) autoPromote(ctx context.Context) error {
	return storage.Coordinate(ctx, s.DB, "jobs.qualify_incident", struct{}{}, nil)
}
