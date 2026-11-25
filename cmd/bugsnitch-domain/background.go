// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/integration"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

type selectedJob struct {
	storage.JobClaim
	Lane string `json:"lane"`
}

const rescan = 5 * time.Second

func signalWork(wake chan<- struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// Reconcile immediately after LISTEN commits, closing the startup/reconnect
// race. Notifications contain no commands. Any lost signal or scheduled retry
// is covered by the scheduler's own timer, even if LISTEN is unavailable.
func watchJobs(ctx context.Context, dsn string, wake chan<- struct{}, progress func(string)) {
	var lastWarning time.Time
	for ctx.Err() == nil {
		listen(ctx, dsn, wake, progress)
		if ctx.Err() == nil && time.Since(lastWarning) >= time.Minute {
			slog.Warn("background notification connection lost, continuing timed queue checks")
			lastWarning = time.Now()
		}
		select {
		case <-time.After(rescan):
		case <-ctx.Done():
			return
		}
	}
}

func listen(ctx context.Context, dsn string, wake chan<- struct{}, progress func(string)) {
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return
	}
	defer db.Close(ctx)
	var channel string
	if db.QueryRow(ctx, "SELECT 'bugsnitch_jobs_' || pg_catalog.md5(current_schema())").Scan(&channel) != nil {
		return
	}
	if _, err = db.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return
	}
	signalWork(wake)
	for ctx.Err() == nil {
		n, err := db.WaitForNotification(ctx)
		if err != nil {
			return
		}
		if n.Payload != "" {
			progress(n.Payload)
		}
		signalWork(wake)
	}
}

type backgroundStores struct {
	db        *sql.DB
	app       *github.App
	incidents incidents.Store
	drafts    integration.Store
	plans     planning.Store
}

// Batch independent inbox transactions while a backlog exists. Every row and
// audit event still commit together. A failure preserves earlier observations.
func (s backgroundStores) processInbox(ctx context.Context) (bool, error) {
	for i := range 16 {
		var processed bool
		if err := storage.Coordinate(ctx, s.db, "jobs.process_incident", struct{}{}, &processed); err != nil {
			return false, err
		}
		if !processed {
			return i > 0, nil
		}
	}
	return true, nil
}

func (s backgroundStores) consume(ctx context.Context, job selectedJob) (bool, error) {
	timeout := 45 * time.Second
	switch job.Lane {
	case "drift":
		timeout = 20 * time.Second
	case "planned_issue":
		timeout = 30 * time.Second
	}
	task, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch job.Lane {
	case "inbox":
		// Processing an observation may make its incident eligible for triage,
		// even if the inbox was drained before reaching the batch bound.
		return s.processInbox(task)
	case "triage":
		var qualified bool
		err := storage.Coordinate(task, s.db, "jobs.qualify_incident", struct{}{}, &qualified)
		return qualified, err
	case "incident_issue":
		return true, s.incidents.SyncIssue(task, s.app, job.JobClaim)
	case "draft":
		return true, s.drafts.SyncDraft(task, job.JobClaim)
	case "planned_issue":
		return true, s.plans.SyncIssue(task, s.app, job.JobClaim)
	case "drift":
		return true, s.plans.SyncDrift(task, s.app, job.JobClaim)
	case "pull_watch":
		return true, s.drafts.SyncWatch(task, job.JobClaim)
	default:
		return false, errors.New("unknown private background queue")
	}
}

func runBackground(ctx context.Context, db *sql.DB, dsn string, app *github.App, progress func(string)) {
	wake := make(chan struct{}, 1)
	var work sync.WaitGroup
	defer work.Wait()
	work.Go(func() { watchJobs(ctx, dsn, wake, progress) })
	stores := backgroundStores{db, app, incidents.Store{DB: db}, integration.Store{DB: db, App: app}, planning.Store{DB: db}}
	lanes := []string{"inbox"}
	if app != nil {
		lanes = append(lanes, "triage", "incident_issue", "draft", "planned_issue", "drift", "pull_watch")
	}
	busy := make(map[string]bool, len(lanes))
	retryAfter := map[string]time.Time{}
	type result struct {
		lane             string
		progress, failed bool
	}
	done := make(chan result, len(lanes))
	ticker := time.NewTicker(rescan)
	defer ticker.Stop()
	signalWork(wake)
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-done:
			delete(busy, r.lane)
			if r.failed {
				retryAfter[r.lane] = time.Now().Add(rescan)
			}
			if r.progress {
				signalWork(wake)
			}
			continue
		case <-ticker.C:
		case <-wake:
		}
		available := make([]string, 0, len(lanes))
		now := time.Now()
		for _, lane := range lanes {
			if !busy[lane] && !now.Before(retryAfter[lane]) {
				available = append(available, lane)
			}
		}
		if len(available) == 0 {
			continue
		}
		var selected []selectedJob
		if err := storage.Coordinate(ctx, db, "jobs.schedule", available, &selected); err != nil {
			if ctx.Err() == nil {
				slog.Warn("background queue selection deferred")
				select {
				case <-time.After(time.Second):
				case <-ctx.Done():
					return
				}
			}
			continue
		}
		for _, j := range selected {
			busy[j.Lane] = true
			work.Go(func() {
				progress, err := stores.consume(ctx, j)
				failed := err != nil && !errors.Is(err, sql.ErrNoRows)
				if failed && ctx.Err() == nil {
					slog.Warn("background job deferred", "queue", j.Lane)
				}
				done <- result{lane: j.Lane, progress: progress && err == nil, failed: failed}
			})
		}
	}
}
