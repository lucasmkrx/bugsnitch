// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

type boardStatusWriter interface {
	SetProjectStatus(context.Context, int64, int64, string, int64, int64, string, string, string) error
}

// Serialize writes to a board item across replicas. If the process exits after
// GitHub accepts a write, the next attempt reads the field before writing it.
func syncBoardStatus(ctx context.Context, db *sql.DB, app boardStatusWriter, active, done string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var workspace, project, repo, board, item, desired string
	var number, githubID, installation, repository int64
	var failures int
	err = tx.QueryRowContext(ctx, `SELECT s.workspace_id,s.project_id,s.number,b.github_id,m.installation_id,m.repository_id,p.repository,
	    b.project_node,b.item_node,s.desired,s.failures
	    FROM github_project_status s JOIN github_project_tickets b USING(workspace_id,project_id,number)
	    JOIN planned_issue_state z USING(workspace_id,project_id,number)
	    JOIN github_mappings m USING(workspace_id,project_id,installation_id,repository_id)
	    JOIN projects p ON (p.workspace_id,p.id)=(s.workspace_id,s.project_id)
	    WHERE s.desired IS DISTINCT FROM s.applied AND s.next_run<=now() AND m.active AND b.github_id=z.github_id
	    ORDER BY s.next_run,s.workspace_id,s.number LIMIT 1 FOR UPDATE OF s SKIP LOCKED FOR SHARE OF m`).
		Scan(&workspace, &project, &number, &githubID, &installation, &repository, &repo, &board, &item, &desired, &failures)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	option := active
	if desired == "done" {
		option = done
	}
	err = app.SetProjectStatus(ctx, installation, repository, repo, number, githubID, board, item, option)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE github_project_status SET applied=desired,failures=0,reason='',next_run=now()
		    WHERE workspace_id=$1 AND project_id=$2 AND number=$3`, workspace, project, number)
	} else {
		delay := min(time.Minute<<min(failures, 6), time.Hour)
		reason := "GitHub status update deferred. Check the App’s Projects permission and the configured Status option."
		var api *github.APIError
		if errors.As(err, &api) {
			delay = max(delay, api.RetryAfter)
		}
		if errors.Is(err, github.ErrSubjectChanged) {
			reason = "GitHub could not match the saved board item, issue or configured Status option."
		}
		_, err = tx.ExecContext(ctx, `UPDATE github_project_status SET failures=least(failures+1,1000),reason=$4,
		    next_run=clock_timestamp()+$5*interval '1 second' WHERE workspace_id=$1 AND project_id=$2 AND number=$3`,
			workspace, project, number, reason, int64(delay/time.Second))
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func boardOptions() (string, string) {
	active, done := os.Getenv("BUGSNITCH_GITHUB_PROJECT_STATUS_ACTIVE"), os.Getenv("BUGSNITCH_GITHUB_PROJECT_STATUS_DONE")
	if active == "" {
		active = "In Progress"
	}
	if done == "" {
		done = "Done"
	}
	return active, done
}

func runBoardSync(ctx context.Context, db *sql.DB, app *github.App) {
	active, done := boardOptions()
	for ctx.Err() == nil {
		task, cancel := context.WithTimeout(ctx, 45*time.Second)
		processed, err := syncBoardStatus(task, db, app, active, done)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("GitHub Projects status reconciliation deferred")
		}
		if processed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(rescan):
		}
	}
}
