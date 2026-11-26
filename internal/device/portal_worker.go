// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

type portalWorker struct {
	project        selectionProject
	digest, target string
	turns          int
	out            io.Writer
}

func PortalWorker(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("portal-worker", flag.ContinueOnError)
	id := fs.String("project", "", "paired project ID")
	target := fs.String("publish-target", "main", "target for owner-approved draft pull requests")
	turns := fs.Int("turns", 16, "maximum implementation, review and correction turns per ticket")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || fs.NArg() != 0 || *turns < 1 || *turns > 512 || !github.ValidBranch(*target) || strings.HasPrefix(*target, "bugsnitch/") {
		return errors.New("use portal-worker --project PROJECT_ID [--publish-target BRANCH] [--turns 1..512]")
	}
	p, err := selectProject(*id)
	if err != nil {
		return err
	}
	digest, err := runner.PortalProject(p.enrolled.Enrollment.Workspace, p.repo, p.path)
	if err != nil {
		return err
	}
	unlock, err := runner.LockUnattended(*id)
	if err != nil {
		return err
	}
	defer unlock()
	w := portalWorker{p, digest, *target, *turns, out}
	fmt.Fprintf(out, "Listening for portal work on %s, targeting %s\n", p.repo, *target)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err = w.poll(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintln(out, err)
			if errors.Is(err, statusError(403)) || errors.Is(err, statusError(401)) || errors.Is(err, runner.ErrAuthorityStale) {
				return err
			}
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	return ctx.Err()
}

func (w portalWorker) exchange(ctx context.Context, req protocol.WorkerStartPoll) (*protocol.WorkerStart, error) {
	req.Protocol, req.Project = protocol.Version, w.project.id
	var response struct {
		Start *protocol.WorkerStart `json:"start"`
	}
	if err := send(ctx, w.project.enrolled, "worker-start", req, &response); err != nil {
		return nil, err
	}
	return response.Start, nil
}

func (w portalWorker) poll(ctx context.Context) error {
	p, err := selectProject(w.project.id)
	if err != nil {
		return err
	}
	if p.path != w.project.path || p.repo != w.project.repo || p.enrolled.Server != w.project.enrolled.Server || p.enrolled.Enrollment.DeviceID != w.project.enrolled.Enrollment.DeviceID || p.enrolled.Enrollment.Workspace != w.project.enrolled.Enrollment.Workspace {
		return runner.ErrAuthorityStale
	}
	w.project = p
	digest, err := runner.PortalProject(p.enrolled.Enrollment.Workspace, p.repo, p.path)
	if err != nil {
		return err
	}
	if digest != w.digest {
		return runner.ErrAuthorityStale
	}
	// A portal admission can already have been acknowledged before a local restart.
	// Resume the exact persisted grant before dequeuing any other ticket.
	raw, err := journalContext(ctx, unattendedSchema+"SELECT id FROM unattended WHERE project="+sqlValue(p.id)+" AND closed=0;")
	if err != nil {
		return err
	}
	if raw != "" {
		status, err := readUnattended(ctx, p.id)
		if err != nil {
			return err
		}
		g := status.Grant
		if !time.Now().Before(g.Expires) {
			return updateUnattended(ctx, g, "Authorization expired", true)
		}
		if !g.PortalDecisions {
			return fmt.Errorf("%w: finish or stop the existing local unattended grant before starting this portal worker", runner.ErrAuthorityStale)
		}
		if g.PublishTarget != w.target || g.ProjectDigest != w.digest || g.TurnBudget > w.turns {
			return runner.ErrAuthorityStale
		}
		return w.supervise(ctx, g.ID)
	}
	req, err := w.exchange(ctx, protocol.WorkerStartPoll{Operation: "poll"})
	if err != nil || req == nil {
		return err
	}
	id, err := hex.DecodeString(req.ID)
	if err != nil || len(id) != 16 || req.Issue < 1 || req.Project != p.id || req.Device != p.enrolled.Enrollment.DeviceID || req.Policy < 1 ||
		!validBindings([]int{req.Issue}, map[int]protocol.IssueRevision{req.Issue: req.Revision}) || !time.Now().Before(req.Expires) {
		return errors.New("service returned an invalid ticket start")
	}
	if req.Status == "planning" && req.Plan == "" {
		return w.plan(ctx, *req)
	}
	if req.Status != "starting" || req.Plan == "" {
		return errors.New("service returned an invalid start phase")
	}
	rec, err := p.unattendedNext(ctx, []int{req.Issue})
	if err != nil {
		return err
	}
	scope := protocol.RunScope{Device: req.Device, Project: req.Project, Plan: req.Plan, Policy: req.Policy}
	g := unattendedGrant{ID: req.ID, Expires: req.Expires, unattendedScope: unattendedScope{
		Scope: scope, Issues: map[int]protocol.IssueRevision{req.Issue: req.Revision}, Workspace: p.enrolled.Enrollment.Workspace,
		Repository: p.repo, Service: p.enrolled.Server, ProjectDigest: w.digest, Parallel: 1, TurnBudget: w.turns,
		Corrections: true, Steward: true, PlanApprover: "owner", PublishTarget: w.target, PortalDecisions: true,
	}}
	if hold := unattendedHold(g, []int{req.Issue}, rec); hold != "" {
		return w.fail(ctx, *req, "dispatch_failed", errors.New(hold))
	}
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	raw, err = journalContext(ctx, `INSERT INTO unattended(project,id,data) VALUES(`+sqlValue(p.id)+","+sqlValue(g.ID)+","+sqlValue(string(data))+`)
		ON CONFLICT(project) DO UPDATE SET id=excluded.id,data=excluded.data,turns=0,closed=0,reason='' WHERE unattended.closed=1 AND unattended.id<>excluded.id;
		SELECT changes();`)
	if err != nil {
		return err
	}
	if raw != "1" {
		return w.fail(ctx, *req, "dispatch_failed", errors.New("this local execution grant already closed"))
	}
	return w.supervise(ctx, g.ID)
}

func (w portalWorker) supervise(ctx context.Context, id string) error {
	err := superviseUnattended(ctx, w.project.id, w.out)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	// The service resolves admitted attempts before processing failure. A local
	// hold before admission therefore produces an actionable terminal receipt.
	_, err = w.exchange(ctx, protocol.WorkerStartPoll{ID: id, Operation: "fail", Reason: "dispatch_failed"})
	return err
}

const portalPlans = `CREATE TABLE IF NOT EXISTS portal_planning(id TEXT PRIMARY KEY, proposal TEXT NOT NULL DEFAULT '');`

func (w portalWorker) plan(ctx context.Context, req protocol.WorkerStart) error {
	raw, err := journalContext(ctx, portalPlans+"INSERT OR IGNORE INTO portal_planning(id) VALUES("+sqlValue(req.ID)+"); SELECT changes();")
	if err != nil {
		return err
	}
	var doc domain.Plan
	if raw == "1" {
		doc, err = runner.PlanPortalTicket(ctx, w.project.enrolled.Enrollment.Workspace, w.project.repo, req)
		if err != nil {
			return w.fail(ctx, req, "planning_failed", err)
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if _, err = journalContext(ctx, "UPDATE portal_planning SET proposal="+sqlValue(string(b))+" WHERE id="+sqlValue(req.ID)+";"); err != nil {
			return err
		}
	} else {
		raw, err = journalContext(ctx, "SELECT proposal FROM portal_planning WHERE id="+sqlValue(req.ID)+";")
		if err != nil {
			return err
		}
		if raw == "" {
			return w.fail(ctx, req, "planning_failed", errors.New("planning was interrupted before the proposal was recorded"))
		}
		if err = json.Unmarshal([]byte(raw), &doc); err != nil {
			return err
		}
	}
	_, err = w.exchange(ctx, protocol.WorkerStartPoll{ID: req.ID, Operation: "proposal", Proposal: &doc})
	if errors.Is(err, statusError(400)) {
		return w.fail(ctx, req, "proposal_invalid", err)
	}
	return err
}

func (w portalWorker) fail(ctx context.Context, s protocol.WorkerStart, reason string, cause error) error {
	_, err := w.exchange(ctx, protocol.WorkerStartPoll{Operation: "fail", ID: s.ID, Reason: reason})
	return errors.Join(cause, err)
}
