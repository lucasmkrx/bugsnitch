// SPDX-License-Identifier: MPL-2.0
package integration

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func (s Store) Watch(ctx context.Context, workspace, project string, number, version int64, watching bool) error {
	if number < 1 || version < 1 {
		return ErrInvalid
	}
	if watching && s.App == nil {
		return ErrUnavailable
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	var open, enabled bool
	err = tx.QueryRowContext(ctx, `SELECT version,coalesce(snapshot->>'state'='OPEN',false),watching
	    FROM pull_observations WHERE workspace_id=$1 AND project_id=$2 AND number=$3 FOR UPDATE`,
		workspace, project, number).Scan(&current, &open, &enabled)
	if err != nil {
		return err
	}
	if current != version || watching && !open {
		return ErrConflict
	}
	if enabled == watching {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE pull_observations SET watching=$4,next_watch=now()
	    WHERE workspace_id=$1 AND project_id=$2 AND number=$3`, workspace, project, number, watching); err != nil {
		return err
	}
	kind := "pull.watch_stopped"
	if watching {
		kind = "pull.watch_started"
	}
	ctx = audit.WithDetails(ctx, audit.Details{SubjectVersion: version})
	if err = audit.Record(ctx, tx, workspace, project, kind, strconv.FormatInt(number, 10)); err != nil {
		return err
	}
	return tx.Commit()
}

// SyncWatch reads only explicitly watched PRs and uses the same inspection
// and compare-and-swap path as an interactive refresh.
func (s Store) SyncWatch(ctx context.Context, job storage.JobClaim) error {
	if job.Item != "" {
		return s.initializeDraftWatch(ctx, job)
	}
	var issue, version int64
	err := s.DB.QueryRowContext(ctx, `SELECT issue_number,version FROM pull_observations
	    WHERE workspace_id=$1 AND project_id=$2 AND number=$3 AND watching
	      AND snapshot->>'state'='OPEN' AND next_watch=$4::timestamptz FOR SHARE`,
		job.Workspace, job.Project, job.Number, job.Fence).Scan(&issue, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx = audit.WithActor(ctx, audit.Service, "pull-watch")
	current, err := s.Refresh(ctx, job.Workspace, job.Project, job.Number, issue, version)
	if errors.Is(err, ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.advanceQueue(ctx, job.Workspace, job.Project, job.Number, current)
}

func (s Store) initializeDraftWatch(ctx context.Context, job storage.JobClaim) error {
	var issue int64
	err := s.DB.QueryRowContext(ctx, `SELECT issue_number FROM draft_pr_intents
		WHERE workspace_id=$1 AND project_id=$2 AND attempt_id=$3 AND number=$4
		  AND status='created' AND follow_at=$5::timestamptz`,
		job.Workspace, job.Project, job.Item, job.Number, job.Fence).Scan(&issue)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx = audit.WithActor(ctx, audit.Service, "pull-watch")
	_, err = s.refresh(ctx, job.Workspace, job.Project, job.Number, issue, 0, true)
	if errors.Is(err, ErrConflict) {
		return nil
	}
	return err
}
