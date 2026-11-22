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
		if err := s.processOne(ctx); err != nil {
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
	if code := signedEvent(s, errorPayload(eventOne, "v1", "production", time.Now())); code != 202 {
		t.Fatalf("observation HTTP %d", code)
	}
	mustExec(t, s.DB, "ALTER TABLE events ADD CONSTRAINT reject_projection CHECK (kind<>'incident.observed')")
	if err := s.processOne(ctx); err == nil {
		t.Fatal("expected audit rejection")
	}
	var counted, processed int
	if err := s.DB.QueryRow("SELECT (SELECT count(*) FROM incidents), (SELECT count(*) FROM incident_inbox WHERE processed)").Scan(&counted, &processed); err != nil || counted != 0 || processed != 0 {
		t.Fatalf("partial projection persisted: incidents=%d processed=%d error=%v", counted, processed, err)
	}
	mustExec(t, s.DB, "ALTER TABLE events DROP CONSTRAINT reject_projection")
	if err := s.processOne(ctx); err != nil {
		t.Fatal(err)
	}
	if inc := one(t, s); inc.Occurrences != 1 {
		t.Fatalf("replay counted %d occurrences", inc.Occurrences)
	}
}
