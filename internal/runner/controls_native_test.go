// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/journalbridge"
)

// Seed exactly the release-v2 journal row, as it exists in a pre-upgrade
// profile. A stale worker one revision behind the active owner must never
// complete a receipt while failing to update the attempt itself.
func TestStaleSettlementPreservesRunningControl(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	a := Attempt{Summary: Summary{ID: strings.Repeat("a", 32), Version: 2, State: "running", Phase: "plan"}}
	if _, err := query("INSERT INTO journal_attempts VALUES(" + literal(a.ID) + ",'personal','fixture/selected',1,2," + packed(a) + ",1)"); err != nil {
		t.Fatal(err)
	}
	ack, err := ApplyControl("stop-receipt", a.ID, "stop", 2)
	if err != nil || ack != "received" {
		t.Fatal(ack, err)
	}
	if ack, err = ApplyControl("stop-receipt", a.ID, "pause", 2); err != nil || ack != "unavailable" {
		t.Fatal(ack, err)
	}
	stale := a
	stale.Version--
	stale.State = "waiting"
	if _, err = settle(&stale); err == nil {
		t.Fatal("stale owner settled a stop")
	}
	if ack, err = ApplyControl("stop-receipt", a.ID, "stop", 2); err != nil || ack != "received" {
		t.Fatal(ack, err)
	}
	a.State = "waiting"
	if applied, err := settle(&a); err != nil || !applied || a.State != "cancelled" || a.Version != 3 {
		t.Fatal(a.Summary, err)
	}
	if ack, err = ApplyControl("stop-receipt", a.ID, "stop", 2); err != nil || ack != "completed" {
		t.Fatal(ack, err)
	}
}

func TestWaitingStopResumesPendingReleaseAfterRestart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUGSNITCH_HOME", dir)
	a := Attempt{Summary: Summary{ID: strings.Repeat("b", 32), Version: 1, State: "waiting", Phase: "plan"}}
	// Simulate the release before Rust owned controls: a waiting attempt was
	// stopped but the release of its cloud reservation was never confirmed.
	a.Version, a.State = 2, "cancelled"
	if _, err := journalbridge.Query(context.Background(), filepath.Join(dir, "runner.sqlite"),
		"INSERT INTO journal_attempts VALUES("+literal(a.ID)+",'personal','fixture/selected',1,2,"+packed(a)+",1); INSERT INTO journal_controls VALUES('old-stop',"+literal(a.ID)+",'stop',1,'finish_pending');"); err != nil {
		t.Fatal(err)
	}
	previous := AfterFinish
	t.Cleanup(func() { AfterFinish = previous })
	AfterFinish = func(Summary) error { return errors.New("unreachable") }
	if ack, err := ApplyControl("old-stop", a.ID, "stop", 1); err != nil || ack != "received" {
		t.Fatal(ack, err)
	}
	releases := 0
	AfterFinish = func(Summary) error { releases++; return nil }
	for range 2 {
		if ack, err := ApplyControl("old-stop", a.ID, "stop", 1); err != nil || ack != "completed" {
			t.Fatal(ack, err)
		}
	}
	if releases != 1 {
		t.Fatal("duplicate release", releases)
	}
	stored, err := Get(a.ID)
	if err != nil || stored.Version != 2 || stored.State != "cancelled" {
		t.Fatal(stored.Summary, err)
	}
}

func TestRejectedCheckpointKeepsObservedVersion(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	a := Attempt{Summary: Summary{ID: strings.Repeat("a", 32), Version: 1, State: "waiting", Workspace: "personal", Repository: "owner/repo", IssueNumber: 1}, UpdatedAt: "observed"}
	if _, err := query("INSERT INTO journal_attempts VALUES(" + literal(a.ID) + ",'personal','owner/repo',1,1," + packed(a) + ",1)"); err != nil {
		t.Fatal(err)
	}
	stale := a
	if err := a.save(); err != nil {
		t.Fatal(err)
	}
	stale.Message = "stale write"
	if err := stale.save(); err == nil || stale.Version != 1 || stale.UpdatedAt != "observed" {
		t.Fatalf("stale save returned %v with version %d, updated at %q", err, stale.Version, stale.UpdatedAt)
	}
	disk, err := Get(a.ID)
	if err != nil || disk.Message != "" || disk.Version != 2 || disk.UpdatedAt != a.UpdatedAt {
		t.Fatalf("winner lost, version %d: %v", disk.Version, err)
	}
}
