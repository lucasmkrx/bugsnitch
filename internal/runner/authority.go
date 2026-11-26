// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
)

var CheckWorker func(context.Context, Summary, bool) (time.Duration, error)
var ErrDeviceUnauthorized = errors.New("original device or project authorization unavailable")
var ErrAuthorityUnreachable = errors.New("worker authority could not be renewed")
var ErrAuthorityStale = errors.New("worker admission changed or dispatch suspended")
var ErrControlPending = errors.New("owner control pending")

func failureCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDeviceUnauthorized):
		return "device_unauthorized"
	case errors.Is(err, ErrAuthorityStale):
		return "authority_stale"
	case errors.Is(err, ErrUnattendedBudget):
		return "allowance_exhausted"
	case errors.Is(err, ErrAuthorityUnreachable):
		return "authority_unreachable"
	case errors.Is(err, ErrControlPending):
		return "control_pending"
	case errors.Is(err, agent.SignIn):
		return "sign_in_required"
	case errors.Is(err, agent.UnavailableModel):
		return "model_unavailable"
	case errors.Is(err, agent.Allowance):
		return "allowance_exhausted"
	case errors.Is(err, agent.RateLimit):
		return "provider_rate_limited"
	case errors.Is(err, agent.Provider), errors.Is(err, agent.ClaudeKeyRequired), errors.Is(err, agent.OpenAIKeyRequired):
		return "provider_unavailable"
	case errors.Is(err, errModelCapacity), errors.Is(err, ErrHostedTurnCapacity):
		return "local_capacity"
	default:
		return "task_failed"
	}
}
func subject(a *Attempt) Summary {
	s := a.Summary
	s.LocalPath = a.Project.Path
	s.Scope = a.LocalRunScope
	return s
}
func lease(ctx context.Context, s Summary, turn bool) (time.Duration, error) {
	if CheckWorker == nil {
		return 0, nil
	}
	started := time.Now()
	check, stop := context.WithTimeout(ctx, 4*time.Second)
	defer stop()
	duration, err := CheckWorker(check, s, turn)
	if err != nil {
		return 0, err
	}
	if duration == 0 {
		return 0, nil
	}
	left := remainingLease(started.Add(duration))
	if left <= 0 {
		return 0, ErrAuthorityUnreachable
	}
	return left, nil
}

// Monotonic time protects ordinary scheduling; wall time also catches machines
// whose monotonic clock pauses during sleep. A delayed renewal cannot bridge a gap.
func remainingLease(expires time.Time) time.Duration {
	now := time.Now()
	return min(expires.Sub(now), expires.Round(0).Sub(now.Round(0)))
}

// A zero lease is possible only for an initially standalone attempt. Once a
// device grant exists, unpairing cannot downgrade a running stage to standalone.
func authorizedStage(ctx context.Context, a *Attempt, turn bool, work func(context.Context) error) error {
	s := subject(a)
	duration, err := lease(ctx, s, turn)
	if err != nil {
		return err
	}
	if duration == 0 {
		return work(ctx)
	}
	running, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	expires := time.Now().Add(duration)
	go func() {
		defer close(done)
		for {
			timer := time.NewTimer(duration / 2)
			select {
			case <-running.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			remaining := remainingLease(expires)
			if remaining <= 0 {
				cancel(ErrAuthorityUnreachable)
				return
			}
			deadline, stop := context.WithTimeout(running, remaining)
			left, err := lease(deadline, s, turn)
			stop()
			if running.Err() != nil {
				return
			}
			if err != nil {
				cancel(err)
				return
			}
			if remainingLease(expires) <= 0 {
				cancel(ErrAuthorityUnreachable)
				return
			}
			if left == 0 {
				cancel(ErrDeviceUnauthorized)
				return
			}
			duration = left
			expires = time.Now().Add(left)
		}
	}()
	workErr := work(running)
	cause := context.Cause(running)
	cancel(nil)
	<-done
	if cause != nil {
		return errors.Join(cause, workErr)
	}
	if remainingLease(expires) <= 0 {
		return errors.Join(ErrAuthorityUnreachable, workErr)
	}
	if workErr != nil {
		return workErr
	}
	left, err := lease(ctx, s, turn)
	if err != nil {
		return err
	}
	if left == 0 {
		return ErrDeviceUnauthorized
	}
	return nil
}
