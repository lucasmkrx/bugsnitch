// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestWorkerRevocationStopsActiveTurnBeforeRelease(t *testing.T) {
	_, issue := fixture(t)
	old, end := CheckWorker, AfterTurn
	t.Cleanup(func() { CheckWorker = old; AfterTurn = end })
	var turns, checks atomic.Int32
	var exited atomic.Bool
	CheckWorker = func(_ context.Context, s Summary, turn bool) (time.Duration, error) {
		if turn && checks.Add(1) > 1 {
			return 0, ErrDeviceUnauthorized
		}
		return 100 * time.Millisecond, nil
	}
	modelTurn = func(ctx context.Context, _ *Attempt, _, _ string, _ any) (agent.Result, error) {
		turns.Add(1)
		<-ctx.Done()
		exited.Store(true)
		return agent.Result{}, ctx.Err()
	}
	AfterTurn = func(Summary) error {
		if !exited.Load() {
			t.Fatal("permit released before model process returned")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := Start(ctx, "personal", issue.Repository, issue)
	if !errors.Is(err, ErrDeviceUnauthorized) || a.State != "waiting" || a.ReasonCode != "device_unauthorized" || turns.Load() != 1 {
		t.Fatal(a.Summary, err, turns.Load())
	}
	saved, err := Summaries()
	if err != nil || len(saved) != 1 || saved[0].ReasonCode != a.ReasonCode || saved[0].ID != a.ID {
		t.Fatal("missing durable revocation", saved, err)
	}
}

func TestWorkerLeaseLossQuiescesValidatorAndRetainsWork(t *testing.T) {
	dir, issue := fixture(t)
	script := []byte("#!/bin/sh\nset -eu\nprintf retained > retained.log\nsleep 30\nprintf done > unexpected.log\n")
	if err := os.WriteFile(filepath.Join(dir, "check.sh"), script, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "commit", "-am", "reviewed long-running validator"); err != nil {
		t.Fatal(err)
	}
	old := CheckWorker
	t.Cleanup(func() { CheckWorker = old })
	CheckWorker = func(_ context.Context, s Summary, _ bool) (time.Duration, error) {
		if s.Phase == "validate" {
			if _, err := os.Stat(filepath.Join(os.Getenv("BUGSNITCH_HOME"), "worktrees", s.ID, "retained.log")); err == nil {
				return 0, ErrAuthorityUnreachable
			}
		}
		// Use the actual lease window so CPU contention during Git and
		// validator startup cannot revoke the grant before the fixture runs.
		return protocol.WorkerLeaseSeconds * time.Second, nil
	}
	modelTurn = func(_ context.Context, a *Attempt, _, _ string, _ any) (agent.Result, error) {
		switch a.Phase {
		case "plan":
			return agent.Result{Text: "Change the label and validate it."}, nil
		case "implement":
			return agent.Result{Text: `{"summary":"Fixed label","edits":[{"path":"example.txt","content":"fixed\n"}]}`}, nil
		default:
			t.Fatal("post-interruption review ran")
			return agent.Result{}, nil
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	a, err := Start(ctx, "personal", issue.Repository, issue)
	if err != nil {
		t.Fatal(err)
	}
	a, err = Continue(ctx, a.ID, "Proceed")
	if !errors.Is(err, ErrAuthorityUnreachable) || ctx.Err() != nil || a.State != "waiting" || a.Phase != "validate" || a.ReasonCode != "authority_unreachable" {
		t.Fatal(a.Summary, err)
	}
	if a.CandidateSHA == "" || a.CandidateSHA == a.BaseSHA {
		t.Fatal("missing candidate")
	}
	evidence := a.Artifacts[len(a.Artifacts)-1]
	if evidence.Phase != "validate" || evidence.ExitCode == nil || *evidence.ExitCode == 0 {
		t.Fatal("missing interrupted validator exit", evidence)
	}
	if b, err := os.ReadFile(filepath.Join(a.Worktree, "retained.log")); err != nil || string(b) != "retained" {
		t.Fatal("dirty work lost", err)
	}
	if _, err := os.Stat(filepath.Join(a.Worktree, "unexpected.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validator continued after cancellation", err)
	}
	saved, err := Get(a.ID)
	if err != nil || saved.Version != a.Version || saved.ReasonCode != a.ReasonCode {
		t.Fatal("journal mismatch", err)
	}
}

func TestWorkerReasonCodesAreBounded(t *testing.T) {
	for err, want := range map[error]string{agent.SignIn: "sign_in_required", agent.Allowance: "allowance_exhausted", agent.UnavailableModel: "model_unavailable", agent.RateLimit: "provider_rate_limited", agent.Provider: "provider_unavailable", ErrDeviceUnauthorized: "device_unauthorized", ErrAuthorityUnreachable: "authority_unreachable", ErrAuthorityStale: "authority_stale", ErrControlPending: "control_pending", errModelCapacity: "local_capacity", ErrHostedTurnCapacity: "local_capacity", errors.New("SYNTHETIC_SECRET"): "task_failed"} {
		if got := failureCode(errors.Join(err, context.Canceled)); got != want || !protocol.ValidReason(got) {
			t.Fatal(got, want)
		}
	}
	if protocol.ValidReason("SYNTHETIC_SECRET") {
		t.Fatal("accepted arbitrary runtime text")
	}
}

func TestSlowWorkerRenewalCannotBridgeAnExpiredGrant(t *testing.T) {
	old := CheckWorker
	t.Cleanup(func() { CheckWorker = old })
	var calls atomic.Int32
	CheckWorker = func(ctx context.Context, s Summary, turn bool) (time.Duration, error) {
		if calls.Add(1) > 1 {
			// A transport returning stale success just after the prior grant's deadline.
			<-ctx.Done()
			return time.Second, nil
		}
		return 100 * time.Millisecond, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := authorizedStage(ctx, &Attempt{}, true, func(c context.Context) error { <-c.Done(); return c.Err() })
	if !errors.Is(err, ErrAuthorityUnreachable) || ctx.Err() != nil {
		t.Fatal("late renewal extended authority", err)
	}
}
