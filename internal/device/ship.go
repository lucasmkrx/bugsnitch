// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

type ShipReceipt struct {
	ApprovalVersion int64  `json:"approvalVersion,omitempty"`
	Status          string `json:"status"`
	Number          int64  `json:"number,omitempty"`
	URL             string `json:"url,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type deliveryRequest struct {
	ApprovalVersion int64  `json:"approvalVersion,omitempty"`
	Protocol        int    `json:"protocol"`
	Operation       string `json:"operation"`
	Attempt         string `json:"attemptId"`
	Version         int    `json:"version"`
	Base            string `json:"baseSha"`
	Candidate       string `json:"candidateSha"`
	Target          string `json:"target"`
	Follow          bool   `json:"follow,omitempty"`
}

func draftRequest(a runner.Attempt, target string) deliveryRequest {
	return deliveryRequest{Protocol: 1, Attempt: a.ID, Version: a.Version, Base: a.BaseSHA, Candidate: a.CandidateSHA, Target: target}
}

func Ship(ctx context.Context, id string, version int, candidate, target string, wait time.Duration, notice func(string)) (ShipReceipt, error) {
	var result ShipReceipt
	if wait <= 0 || wait > time.Hour || !github.ValidBranch(target) || strings.HasPrefix(target, "bugsnitch/") || version < 1 {
		return result, errors.New("choose a valid target, positive version and a positive wait of at most one hour")
	}
	a, err := runner.Get(id)
	if err != nil {
		return result, err
	}
	if a.ID != id || a.Steward != nil || a.State != "completed" || a.Phase != "wrap_up" || a.Version != version || a.CandidateSHA == "" || a.CandidateSHA != candidate {
		return result, errors.New("local-ship requires the exact reviewed attempt that will open the new draft")
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err = syncOnce(ctx); err != nil {
		return result, err
	}
	sub := a.Summary
	sub.LocalPath, sub.Scope = a.Project.Path, a.LocalRunScope
	p, _, err := workerBinding(ctx, sub, false)
	if err != nil {
		return result, err
	}
	if p == nil {
		return result, runner.ErrDeviceUnauthorized
	}
	req := draftRequest(a, target)
	step := func(ctx context.Context, op string) (ShipReceipt, error) {
		req.Operation = op
		var state ShipReceipt
		err := send(ctx, *p, "draft-delivery", req, &state)
		if err == nil && state.Status != "awaiting_approval" {
			if state.ApprovalVersion < 1 || req.ApprovalVersion != 0 && state.ApprovalVersion != req.ApprovalVersion {
				return state, statusError(409)
			}
			req.ApprovalVersion = state.ApprovalVersion
		}
		return state, err
	}
	publish := func(ctx context.Context) error {
		_, err := runner.PublishApprovedDraft(ctx, id, version, candidate, target, func(ctx context.Context) error {
			state, err := step(ctx, "status")
			if err != nil {
				return err
			}
			if state.Status != "awaiting_publication" {
				return errors.New("owner approval changed before publication")
			}
			return nil
		})
		return err
	}
	result, err = awaitDraft(ctx, a.Repository, step, publish, 3*time.Second, notice)
	if err != nil {
		return result, fmt.Errorf("%w. To reconcile the same delivery, run bugsnitch local-ship --id %s --version %d --candidate %s --target %s", err, id, version, candidate, target)
	}
	return result, nil
}

// Only the owner can create the approval. Re-read the durable state after an
// uncertain release. The App worker independently verifies both GitHub refs.
func awaitDraft(ctx context.Context, repo string, step func(context.Context, string) (ShipReceipt, error), publish func(context.Context) error, interval time.Duration, notice func(string)) (ShipReceipt, error) {
	var state ShipReceipt
	var previous string
	published := false
	for {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		seen, err := step(ctx, "status")
		if err == nil {
			state = seen
			if previous != state.Status && notice != nil {
				notice(state.Status)
			}
			previous = state.Status
			switch state.Status {
			case "awaiting_approval", "queued", "reconciling":
			case "awaiting_publication":
				if !published {
					if err := publish(ctx); err != nil {
						return state, err
					}
					published = true
				}
				_, err = step(ctx, "published")
			case "created":
				if state.Number < 1 {
					return state, errors.New("draft receipt is missing a GitHub PR number")
				}
				state.URL = fmt.Sprintf("https://github.com/%s/pull/%d", repo, state.Number)
				return state, nil
			case "blocked", "withdrawn":
				return state, fmt.Errorf("draft delivery stopped: %s", state.Reason)
			default:
				return state, errors.New("unsupported draft delivery response")
			}
		}
		var status statusError
		if errors.As(err, &status) && status >= 400 && status < 500 && status != 429 {
			return state, err
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-time.After(interval):
		}
	}
}
