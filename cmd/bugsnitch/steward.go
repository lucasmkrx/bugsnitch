// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"errors"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func steward(ctx context.Context, id string, version int, candidate string, number int, watch time.Duration, publish bool) error {
	if watch < 0 || watch > 24*time.Hour {
		return errors.New("watch duration must be between zero and 24 hours")
	}
	if watch > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, watch)
		defer cancel()
	}
	for {
		a, err := runner.Steward(ctx, id, version, candidate, number)
		if watch > 0 && errors.Is(err, runner.ErrStewardFinished) {
			return printJSON(map[string]string{"status": "finished", "reason": err.Error()})
		}
		if watch > 0 && errors.Is(err, runner.ErrStewardIdle) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(30 * time.Second):
				continue
			}
		}
		if err != nil {
			return err
		}
		if err = printJSON(a.Summary); err != nil {
			return err
		}
		if !publish || a.State != "completed" {
			return nil
		}
		receipt, err := runner.PublishCandidate(ctx, a.ID, a.Version, a.CandidateSHA)
		if err != nil {
			return err
		}
		if err = printJSON(receipt); err != nil {
			return err
		}
		if err = runner.PostStewardNote(ctx, a.ID, receipt); err != nil {
			return err
		}
		if watch == 0 {
			return nil
		}
		id, version, candidate = a.ID, a.Version, a.CandidateSHA
	}
}
