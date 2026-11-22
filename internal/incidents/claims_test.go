// SPDX-License-Identifier: MPL-2.0
package incidents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func TestPostgresFencedIncidentClaim(t *testing.T) {
	s := disposableStore(t)
	ctx := context.Background()
	id, err := s.Report(ctx, "personal", "p", "Cart is empty", "Cart retains item", "Add an item", "claim-test-request")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Promote(ctx, "personal", "p", id, 1); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	const workers = 8
	winners := make(chan storage.JobClaim, workers)
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start
			claim, err := storage.ClaimJob(ctx, s.DB, "incident_issue")
			if err == nil {
				winners <- claim
			} else if !errors.Is(err, sql.ErrNoRows) {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("%d claims of a single due row", len(winners))
	}
	lost := <-winners
	if lost.Item != id || lost.Workspace != "personal" || lost.Project != "p" || lost.Fence == "" {
		t.Fatalf("missing scoped identity or fence: %+v", lost)
	}

	// Model a lost claim reply. Even the previous server release sees no due
	// job until the deadline. Expiring the deadline lets another worker recover.
	var due int
	if err = s.DB.QueryRow("SELECT count(*) FROM incident_issues WHERE next_run<=now()").Scan(&due); err != nil || due != 0 {
		t.Fatalf("legacy worker could see claimed row: due=%d err=%v", due, err)
	}
	expired := lost
	if err = s.DB.QueryRow("UPDATE incident_issues SET next_run=now()-interval '1 second' WHERE incident_id=$1 RETURNING next_run::text", id).Scan(&expired.Fence); err != nil {
		t.Fatal(err)
	}
	if err = s.syncClaimed(ctx, nil, expired); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired claim with a matching stored fence accepted: %v", err)
	}
	next, err := storage.ClaimJob(ctx, s.DB, "incident_issue")
	if err != nil || next.Fence == lost.Fence {
		t.Fatalf("missing fresh claim after expiry: %+v, %v", next, err)
	}
	// A nil app would panic if the stale or revoked consumer reached provider I/O.
	if err = s.syncClaimed(ctx, nil, lost); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("superseded worker accepted: %v", err)
	}
	wrong := next
	wrong.Project = "other"
	if err = s.syncClaimed(ctx, nil, wrong); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong project accepted: %v", err)
	}
	mustExec(t, s.DB, "UPDATE github_mappings SET active=false WHERE workspace_id='personal' AND project_id='p'")
	if err = s.syncClaimed(ctx, nil, next); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked repository mapping accepted: %v", err)
	}
}

func TestPostgresNativeIncidentQualification(t *testing.T) {
	s := disposableStore(t)
	ctx := context.Background()
	mustExec(t, s.DB, "UPDATE projects SET auto_triage=true WHERE id='p'")
	for i, release := range []string{"v1", "v2", "v1", "v1"} {
		if code := signedEvent(s, errorPayload(fmt.Sprintf("%032x", i+1), release, "production", time.Now())); code != 202 {
			t.Fatalf("observation %d HTTP %d", i, code)
		}
		if err := s.processAccepted(ctx); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if err := s.autoPromote(ctx); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		var intents, events int
		if err := s.DB.QueryRow("SELECT (SELECT count(*) FROM incident_issues), (SELECT count(*) FROM events WHERE kind='incident.policy_qualified' AND actor_source='service' AND actor_id='incident-worker' AND policy_version IS NOT NULL)").Scan(&intents, &events); err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 3 {
			want = 1
		}
		if intents != want || events != want {
			t.Fatalf("after releases through %d, got %d intents and %d policy events, want %d each", i, intents, events, want)
		}
	}
}

func TestPostgresNativeInboxRollsBackWithoutEvent(t *testing.T) {
	s := disposableStore(t)
	ctx := context.Background()
	mustExec(t, s.DB, "ALTER TABLE events ADD CONSTRAINT reject_projection CHECK (kind<>'incident.observed')")
	if code := signedEvent(s, errorPayload(eventOne, "v1", "production", time.Now())); code != 202 {
		t.Fatalf("observation HTTP %d", code)
	}
	if err := s.processOne(ctx); err == nil {
		t.Fatal("expected audit rejection")
	}
	var counted, processed int
	if err := s.DB.QueryRow("SELECT (SELECT count(*) FROM incidents), (SELECT count(*) FROM incident_inbox WHERE processed)").Scan(&counted, &processed); err != nil || counted != 0 || processed != 0 {
		t.Fatalf("partial projection persisted: incidents=%d processed=%d error=%v", counted, processed, err)
	}
	mustExec(t, s.DB, "ALTER TABLE events DROP CONSTRAINT reject_projection")
	if err := s.processAccepted(ctx); err != nil {
		t.Fatal(err)
	}
	if inc := one(t, s); inc.Occurrences != 1 {
		t.Fatalf("replay counted %d occurrences", inc.Occurrences)
	}
}

// Two independent dispatchers can reserve different lanes in the same scan.
// Busy lanes are omitted, and a dropped reply keeps the reservations fenced.
func TestPostgresBackgroundScheduling(t *testing.T) {
	s := disposableStore(t)
	ctx := context.Background()
	id, err := s.Report(ctx, "personal", "p", "Missing total", "Show total", "Open basket", "scheduled-incident")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Promote(ctx, "personal", "p", id, 1); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id) VALUES('personal','p',1,11,88,99),('personal','p',2,12,88,99)`)
	type job struct {
		storage.JobClaim
		Lane string
	}
	schedule := func(lanes ...string) ([]job, error) {
		var jobs []job
		err := storage.Coordinate(ctx, s.DB, "jobs.schedule", lanes, &jobs)
		return jobs, err
	}
	// An omitted lane must not acquire a lease even when immediately due.
	jobs, err := schedule("inbox", "triage")
	if err != nil || len(jobs) != 0 {
		t.Fatalf("unrequested work: %v %v", jobs, err)
	}
	var group sync.WaitGroup
	winners := make(chan []job, 2)
	for range 2 {
		group.Go(func() {
			batch, err := schedule("incident_issue", "draft", "planned_issue", "drift")
			if err != nil {
				t.Error(err)
			}
			winners <- batch
		})
	}
	group.Wait()
	close(winners)
	got := map[string]int{}
	for batch := range winners {
		inBatch := map[string]bool{}
		for _, j := range batch {
			if inBatch[j.Lane] || j.Fence == "" || j.Workspace != "personal" || j.Project != "p" {
				t.Fatalf("invalid reservation: %+v", j)
			}
			inBatch[j.Lane] = true
			got[fmt.Sprint(j.Lane, "/", j.Number, "/", j.Item)]++
		}
	}
	if len(got) != 3 || got["incident_issue/0/"+id] != 1 || got["drift/1/"] != 1 || got["drift/2/"] != 1 {
		t.Fatalf("duplicate or missing claims: %v", got)
	}
	jobs, err = schedule("incident_issue", "drift")
	if err != nil || len(jobs) != 0 {
		t.Fatalf("lost replies were claimed again before lease expiry: %v %v", jobs, err)
	}
	mustExec(t, s.DB, `UPDATE planned_issue_state SET next_check=now(); UPDATE github_mappings SET active=false`)
	jobs, err = schedule("drift")
	if err != nil || len(jobs) != 0 {
		t.Fatalf("inactive mapping selected: %v %v", jobs, err)
	}
}

// The packaged listener must recover durable queued work even when PG_NOTIFY is
// unavailable. Its provider-disabled profile still processes local observations.
func TestPostgresBackgroundWakeRecovery(t *testing.T) {
	s := disposableStore(t)
	// Verify the listener established a session for this test schema. Drain a
	// signaled observation before isolating recovery from the initial scan.
	deadline := time.Now().Add(8 * time.Second)
	for {
		var connected bool
		err := s.DB.QueryRow(`SELECT EXISTS (SELECT FROM pg_stat_activity WHERE state='idle' AND query=
        'LISTEN "bugsnitch_jobs_'||pg_catalog.md5(current_schema())||'"')`).Scan(&connected)
		if err != nil {
			t.Fatal(err)
		}
		if connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("schema notification listener did not subscribe")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mustExec(t, s.DB, `INSERT INTO incident_inbox(workspace_id,project_id,source,event_key,group_key,observed,seen_at)
        VALUES('personal','p','sentry','initial-wake','wake-group','initial observation',now())`)
	for {
		var processed bool
		if err := s.DB.QueryRow("SELECT processed FROM incident_inbox WHERE event_key='initial-wake'").Scan(&processed); err != nil {
			t.Fatal(err)
		}
		if processed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first queued observation was stranded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mustExec(t, s.DB, "ALTER TABLE incident_inbox DISABLE TRIGGER wake_inbox")
	mustExec(t, s.DB, `INSERT INTO incident_inbox(workspace_id,project_id,source,event_key,group_key,observed,seen_at)
    VALUES('personal','p','sentry','wake-test','wake-group','network unavailable',now())`)
	until := time.Now().Add(12 * time.Second)
	for {
		var observations int
		if err := s.DB.QueryRow("SELECT count(*) FROM events WHERE kind='incident.observed'").Scan(&observations); err != nil {
			t.Fatal(err)
		}
		if observations == 2 {
			return
		}
		if time.Now().After(until) {
			t.Fatal("committed observation was stranded after lost notification")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
