// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func capacityFlag(dst *int) func(string) error {
	return func(raw string) error {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > runner.MaxLocalCapacity {
			return fmt.Errorf("use an integer from 1 to %d", runner.MaxLocalCapacity)
		}
		*dst = n
		return nil
	}
}

// LocalLimits reads or changes the runner's persistent capacity without enrollment.
func LocalLimits(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("local-limits", flag.ContinueOnError)
	var tickets, turns int
	fs.Func("tickets", "maximum concurrent implementation tickets on this runner", capacityFlag(&tickets))
	fs.Func("turns", "maximum simultaneous model turns on this runner", capacityFlag(&turns))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("use local-limits [--tickets 1..32] [--turns 1..32]")
	}
	limits, err := runner.SetLocalLimits(tickets, turns)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(limits)
}

// LocalRun is reached only by a foreground owner command. All model work remains
// on this device and ordinary worktree/admission/turn checks still run.
func LocalRun(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("local-run", flag.ContinueOnError)
	var limit, parallel int
	pr := fs.String("project", "", "assigned project ID")
	fs.Func("limit", "maximum new tickets in this invocation (defaults to this runner's ticket limit)", capacityFlag(&limit))
	fs.Func("parallel", "maximum concurrent attempts (defaults to this runner's ticket limit)", capacityFlag(&parallel))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pr == "" || fs.NArg() != 0 {
		return errors.New("use local-run --project PROJECT_ID [--limit 1..32] [--parallel 1..32]")
	}
	limits, err := runner.SetLocalLimits(0, 0)
	if err != nil {
		return err
	}
	if limit == 0 {
		limit = limits.Tickets
	}
	if parallel == 0 {
		parallel = limits.Tickets
	}
	if parallel > limits.Tickets {
		return fmt.Errorf("--parallel exceeds this runner's ticket limit of %d; use local-limits --tickets to change it", limits.Tickets)
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
	outcomes := make(chan result, parallel)
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
	for launched < limit && run.Err() == nil {
		if active == parallel {
			finish()
			continue
		}
		rec, err := p.recommend(run, parallel > 1)
		if err != nil {
			if active > 0 && errors.Is(err, runner.ErrTicketCapacity) {
				finish()
				continue
			}
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
