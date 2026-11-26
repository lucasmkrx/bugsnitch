// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/integration"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
	"github.com/lucasmkrx/bugsnitch/internal/transport"
)

// Run one bounded pass while Cloud Run allocates CPU to the Scheduler request.
// The database retains pending work, provider reconciliation and retry times.
func runScheduledJobs(ctx context.Context, service *transport.Server, app *github.App) error {
	ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	db := service.DB
	lock, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer lock.Rollback()
	var acquired bool
	if err := lock.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtext(current_schema()), 1874352271)`).Scan(&acquired); err != nil || !acquired {
		return err
	}
	lanes := []string{"inbox"}
	if app != nil {
		lanes = append(lanes, "triage", "incident_issue", "draft", "planned_issue", "drift", "pull_watch")
	}
	var jobs []selectedJob
	if err := storage.Coordinate(ctx, db, "jobs.schedule", lanes, &jobs); err != nil {
		return err
	}
	work := append(make([]func() error, 0, len(jobs)+3), func() error { return storage.Retain(ctx, db) })
	if service.LinearAgent != nil {
		work = append(work, func() error { _, err := service.ProcessLinearSession(ctx); return err })
	}
	if app != nil {
		work = append(work, func() error {
			active, done := boardOptions()
			_, err := syncBoardStatus(ctx, db, app, active, done)
			return err
		})
	}
	stores := backgroundStores{db, app, service.Incidents, integration.Store{DB: db, App: app}, planning.Store{DB: db}}
	for _, job := range jobs {
		work = append(work, func() error { _, err := stores.consume(ctx, job); return err })
	}
	var group sync.WaitGroup
	results := make(chan error, len(work))
	for _, task := range work {
		group.Go(func() { results <- task() })
	}
	group.Wait()
	close(results)
	var failure error
	for err := range results {
		if !errors.Is(err, sql.ErrNoRows) {
			failure = errors.Join(failure, err)
		}
	}
	return failure
}
