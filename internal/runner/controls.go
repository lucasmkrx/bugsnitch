// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/journalbridge"
)

type controlRequest struct {
	Action  string `json:"action"`
	ID      string `json:"id"`
	Command string `json:"command,omitempty"`
	Op      string `json:"op,omitempty"`
	Version int    `json:"version,omitempty"`
	At      string `json:"at,omitempty"`
	Data    string `json:"data,omitempty"`
}

func journalControl(ctx context.Context, request controlRequest) (string, error) {
	dir, err := home()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := journalbridge.Control(ctx, filepath.Join(dir, "runner.sqlite"), request)
	return string(result), err
}

func RequestControl(id, op string) (string, error) {
	a, err := Get(id)
	if err != nil {
		return "", err
	}
	return ApplyControl(fmt.Sprintf("local-%s-%s-%d", id, op, a.Version), id, op, a.Version)
}

// ApplyControl durably binds each command to the exact local subject revision.
// A running process owns completion. Receipt alone never proves it stopped.
func ApplyControl(command, id, op string, version int) (string, error) {
	ctx := context.Background()
	status, err := journalControl(ctx, controlRequest{Action: "apply", ID: id, Command: command, Op: op, Version: version, At: now()})
	if err != nil {
		return "", err
	}
	if status == "finish_pending" {
		a, e := Get(id)
		if e != nil {
			return "", e
		}
		// After a waiting attempt is stopped, release its issue reservation before
		// acknowledging completion. Retain the receipt if the service is unreachable.
		if e = hook(AfterFinish, &a); e != nil {
			return "received", nil
		}
		return journalControl(ctx, controlRequest{Action: "finish", ID: id, Command: command, Version: version})
	}
	return status, nil
}
func boundary(ctx context.Context, id string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	op, err := journalControl(ctx, controlRequest{Action: "pending", ID: id})
	if err != nil {
		return err
	}
	if op != "" {
		return ErrControlPending
	}
	return nil
}
func watchControls(ctx context.Context, id string, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if boundary(ctx, id) != nil {
				cancel()
				return
			}
		}
	}
}

// Commit final state and consume pending controls under the same SQLite lock.
// A crash before this transaction leaves the control in received for reconciliation.
func settle(a *Attempt) (bool, error) {
	if !domain.ValidAttempt(a.Phase, a.State, a.CandidateSHA) {
		return false, errors.New("attempt cannot settle in this phase and state")
	}
	next := *a
	next.Version++
	next.UpdatedAt = now()
	data, err := json.Marshal(&next)
	if err != nil {
		return false, err
	}
	op, err := journalControl(context.Background(), controlRequest{Action: "settle", ID: a.ID, Version: a.Version, Data: string(data)})
	if err != nil {
		return false, err
	}
	switch op {
	case "stale":
		return false, errors.New("attempt changed during finalization")
	case "pause", "stop":
		next.State = "waiting"
		if op == "stop" {
			next.State = "cancelled"
		}
		next.ReasonCode = ""
		next.Message = "Owner control applied"
	case "":
	default:
		return false, errors.New("unrecognized control settlement")
	}
	*a = next
	return op != "", nil
}
