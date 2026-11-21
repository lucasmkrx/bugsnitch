// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
	"io"
)

// LocalRun is reached only by a foreground owner command. All model work remains
// on this device and ordinary worktree/admission/turn checks still run.
func LocalRun(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("local-run", flag.ContinueOnError)
	pr := fs.String("project", "", "assigned project ID")
	limit := fs.Int("limit", 2, "maximum new tickets in this invocation (1–16)")
	parallel := fs.Int("parallel", 2, "maximum concurrent local attempts (1–2)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pr == "" || fs.NArg() != 0 || *limit < 1 || *limit > 16 || *parallel < 1 || *parallel > 2 {
		return errors.New("use local-run --project PROJECT_ID [--limit 1..16] [--parallel 1|2]")
	}
	p, err := selectProject(*pr)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	type update struct {
		Event   string `json:"event"`
		Issue   int    `json:"issue,omitempty"`
		Attempt string `json:"attempt,omitempty"`
		Phase   string `json:"phase,omitempty"`
		State   string `json:"state,omitempty"`
		Reason  string `json:"reason,omitempty"`
	}
	type result struct {
		attempt runner.Attempt
		issue   int
		err     error
	}
	outcomes := make(chan result, *parallel)
	var failures []error
	var active, launched int
	run, stop := context.WithCancel(ctx)
	defer stop()
	emit := func(v update) {
		if err := enc.Encode(v); err != nil {
			failures = append(failures, err)
			stop()
		}
	}
	finish := func() {
		result := <-outcomes
		active--
		a := result.attempt
		u := update{Event: "outcome", Issue: result.issue, Attempt: a.ID, Phase: a.Phase, State: a.State}
		if result.err != nil {
			u.Reason = result.err.Error()
			failures = append(failures, fmt.Errorf("issue #%d: %w", result.issue, result.err))
		} else if a.State == "waiting" || a.State == "cancelled" {
			u.Reason = a.Message
		}
		emit(u)
	}
	var original *protocol.RunScope
	seen := make(map[int]bool)
	for launched < *limit && run.Err() == nil {
		if active == *parallel {
			finish()
			continue
		}
		rec, err := p.recommend(run)
		if err != nil {
			failures = append(failures, err)
			break
		}
		if rec.Selection == nil {
			if active > 0 {
				finish()
				continue
			}
			emit(update{Event: "selection_stopped", Reason: rec.Reason})
			if launched == 0 {
				failures = append(failures, errors.New(rec.Reason))
			}
			break
		}
		scope := rec.Scope
		if scope == nil || scope.Device != p.enrolled.Enrollment.DeviceID || scope.Project != p.id || scope.Plan == "" || scope.Policy < 1 {
			failures = append(failures, errors.New("recommendation lacks current plan, policy or original device binding"))
			break
		}
		if original != nil && *scope != *original {
			failures = append(failures, errors.New("approved plan or policy changed during this local run; inspect the new scope"))
			break
		}
		original = scope
		issue := runner.Issue(*rec.Selection)
		if seen[issue.Number] {
			failures = append(failures, errors.New("service repeated an already selected issue; inspect the original attempt"))
			break
		}
		seen[issue.Number] = true
		active++
		launched++
		admitted := make(chan runner.Summary, 1)
		go func() {
			// One notification per work item. A pre-admission failure closes
			// this channel without authorizing the next recommendation.
			a, e := runner.StartPass(run, p.enrolled.Enrollment.Workspace, issue, *scope, func(s runner.Summary) { admitted <- s })
			close(admitted)
			outcomes <- result{a, issue.Number, e}
		}()
		s, ok := <-admitted
		if !ok {
			break
		}
		emit(update{Event: "admitted", Issue: issue.Number, Attempt: s.ID, Phase: s.Phase, State: s.State})
	}
	for active > 0 {
		finish()
	}
	return errors.Join(append(failures, ctx.Err())...)
}
