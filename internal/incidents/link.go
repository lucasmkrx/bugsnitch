// SPDX-License-Identifier: MPL-2.0
package incidents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/github"
)

// LinkIssue records a verified existing issue without starting the creation outbox.
func (s Store) LinkIssue(ctx context.Context, workspace, project, id string, version, installation, repository int64, repo string, issue github.Issue) error {
	if issue.ID < 1 || issue.Number < 1 || issue.State != "open" || len(issue.PullRequest) > 0 {
		return ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM incidents WHERE workspace_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, workspace, project, id).Scan(&current); err != nil {
		return err
	}
	var policyVersion int64
	err = tx.QueryRowContext(ctx, `SELECT p.policy_version FROM github_mappings m JOIN projects p ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id)
		WHERE m.workspace_id=$1 AND m.project_id=$2 AND m.installation_id=$3 AND m.repository_id=$4 AND m.active AND p.repository=$5 FOR SHARE OF m,p`, workspace, project, installation, repository, repo).Scan(&policyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnmapped
	}
	if err != nil {
		return err
	}
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT status='linked' AND approved_version=$2 AND number=$3 AND issue_id=$4 FROM incident_issues WHERE incident_id=$1`, id, version, issue.Number, issue.ID).Scan(&same)
	if err == nil {
		if same {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != version {
		return ErrConflict
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO incident_issues(workspace_id,project_id,incident_id,installation_id,repository_id,marker,approved_version,status,policy_version,number,issue_id,github_state,requirements_digest,checked_at,next_run)
		VALUES($1,$2,$3,$4,$5,$6,$7,'linked',$8,$9,$10,'open',$11,now(),now()+interval '15 minutes') ON CONFLICT (repository_id,number) DO NOTHING`,
		workspace, project, id, installation, repository, newID(), version, policyVersion, issue.Number, issue.ID, digest(issue.Title+"\x00"+issue.Body))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return err
		}
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE incidents SET version=version+1 WHERE id=$1`, id); err != nil {
		return err
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: "Linked an existing GitHub issue", SubjectVersion: version, PolicyVersion: policyVersion, EvidenceRef: fmt.Sprintf("github-issue/%d", issue.ID)})
	if err = event(ctx, tx, workspace, project, "incident.issue_linked", id); err != nil {
		return err
	}
	return tx.Commit()
}
