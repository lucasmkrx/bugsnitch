// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

// Wakeups carry only a scoped event cursor. The polling path still evaluates
// current facts after a timeout or a connection failure.
func watchProgression(ctx context.Context, p profile, project string, wake chan<- struct{}) {
	var after int64
	for ctx.Err() == nil {
		var current struct {
			Position        int64 `json:"position"`
			NextPollSeconds int   `json:"nextPollSeconds"`
		}
		request := struct {
			Protocol int    `json:"protocol"`
			Project  string `json:"projectId"`
			After    int64  `json:"after"`
		}{protocol.Version, project, after}
		err := send(ctx, p, "progression-wake", request, &current)
		if err == nil && current.Position > after {
			if after != 0 {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
			after = current.Position
			continue
		}
		seconds := max(1, min(current.NextPollSeconds, 60))
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

const progressionSchema = `CREATE TABLE IF NOT EXISTS unattended_actions(
	grant_id TEXT NOT NULL, attempt TEXT NOT NULL, version INTEGER NOT NULL, action TEXT NOT NULL,
	state TEXT NOT NULL, tries INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(grant_id,attempt,version,action)
);
CREATE TABLE IF NOT EXISTS unattended_observations(
	grant_id TEXT NOT NULL, attempt TEXT NOT NULL, action TEXT NOT NULL, PRIMARY KEY(grant_id,attempt)
);`

func (g unattendedGrant) authority() runner.UnattendedAuthority {
	a := runner.UnattendedAuthority{GrantID: g.ID, Digest: g.ProjectDigest, Corrections: g.Corrections, TakeTurn: g.takeTurn}
	if g.PortalDecisions {
		a.FirstAttemptID = g.ID
		a.PublishTarget = g.PublishTarget
	}
	return a
}

func unattendedActions(ctx context.Context, g unattendedGrant) ([]protocol.NextAction, error) {
	raw, err := journalContext(ctx, progressionSchema+"SELECT json_group_array(json(action)) FROM unattended_observations WHERE grant_id="+sqlValue(g.ID)+" ORDER BY attempt;")
	if err != nil {
		return nil, err
	}
	var actions []protocol.NextAction
	return actions, json.Unmarshal([]byte(raw), &actions)
}

func saveNextAction(ctx context.Context, g unattendedGrant, a protocol.NextAction) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = journalContext(ctx, progressionSchema+"INSERT OR REPLACE INTO unattended_observations VALUES("+sqlValue(g.ID)+","+sqlValue(a.AttemptID)+","+sqlValue(string(raw))+");")
	return err
}

// An uncertain operation is retried only when its implementation can reconcile
// the exact prior outcome. Project locking serializes these actions locally.
func (g unattendedGrant) effect(ctx context.Context, a runner.Attempt, key string, reconcile bool, work func() error) error {
	identity := sqlValue(g.ID) + "," + sqlValue(a.ID) + "," + strconv.Itoa(a.Version) + "," + sqlValue(key)
	maximum := 1
	if reconcile {
		maximum = 3
	}
	subject := "(grant_id,attempt,version,action)=(" + identity + ")"
	active := "EXISTS(SELECT 1 FROM unattended WHERE id=" + sqlValue(g.ID) + " AND closed=0 AND unixepoch('subsec')*1000<" + strconv.FormatInt(g.Expires.UnixMilli(), 10) + ")"
	state, err := journalContext(ctx, progressionSchema+`BEGIN IMMEDIATE;
		INSERT OR IGNORE INTO unattended_actions(grant_id,attempt,version,action,state) SELECT `+identity+`,'intent' WHERE `+active+`;
		UPDATE unattended_actions SET tries=tries+1 WHERE `+subject+` AND state='intent' AND tries<`+strconv.Itoa(maximum)+` AND `+active+`;
		SELECT changes();
		COMMIT;`)
	if err != nil {
		return err
	}
	if state != "1" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err = work()
	next := "done"
	if err != nil {
		var status statusError
		if reconcile && !errors.Is(err, runner.ErrAuthorityStale) && !(errors.As(err, &status) && status >= 400 && status < 500 && status != 429) {
			_, holdErr := journalContext(ctx, "UPDATE unattended_actions SET state='held' WHERE "+subject+" AND tries>="+strconv.Itoa(maximum)+";")
			return errors.Join(err, holdErr)
		}
		next = "held"
	}
	_, writeErr := journalContext(ctx, "UPDATE unattended_actions SET state="+sqlValue(next)+" WHERE (grant_id,attempt,version,action)=("+identity+");")
	return errors.Join(err, writeErr)
}

type progressionRequest struct {
	Question     string                      `json:"question,omitempty"`
	PlanSummary  string                      `json:"planSummary,omitempty"`
	Protocol     int                         `json:"protocol"`
	AttemptID    string                      `json:"attemptId"`
	Version      int                         `json:"version"`
	RequestPlan  bool                        `json:"requestPlan,omitempty"`
	Recipient    string                      `json:"recipient,omitempty"`
	Correction   *protocol.CorrectionSubject `json:"correction,omitempty"`
	Prepared     *protocol.PreparedSignoff   `json:"prepared,omitempty"`
	ExpiresAt    *time.Time                  `json:"expiresAt,omitempty"`
	RecoveryHold bool                        `json:"recoveryHold,omitempty"`
}

func hostedNextAction(ctx context.Context, p profile, a runner.Attempt, approver string) (protocol.NextAction, error) {
	req := progressionRequest{Protocol: protocol.Version, AttemptID: a.ID, Version: a.Version, RequestPlan: approver != ""}
	if approver != "owner" {
		req.Recipient = approver
	}
	return fetchProgression(ctx, p, a, req)
}

func fetchProgression(ctx context.Context, p profile, a runner.Attempt, req progressionRequest) (protocol.NextAction, error) {
	var next protocol.NextAction
	err := send(ctx, p, "progression", req, &next)
	if err == nil && (next.AttemptID != a.ID || next.Version != a.Version || next.Candidate != a.CandidateSHA) {
		err = runner.ErrAuthorityStale
	}
	return next, err
}

func observeUnattended(ctx context.Context, p selectionProject, g unattendedGrant, summary runner.Summary) (protocol.NextAction, runner.Attempt, bool, error) {
	a, err := runner.Get(summary.ID)
	if err != nil {
		return protocol.NextAction{}, a, false, err
	}
	if a.UnattendedGrant != g.ID || a.LocalRunScope == nil || *a.LocalRunScope != g.Scope {
		return protocol.NextAction{}, a, false, nil
	}
	if a.State == "running" && runner.UnattendedNeedsInspection(a) {
		if a.Phase == "implement" && a.Intent != nil {
			held, err := journalContext(ctx, progressionSchema+"SELECT state FROM unattended_actions WHERE grant_id="+sqlValue(g.ID)+" AND attempt="+sqlValue(a.ID)+" AND version="+strconv.Itoa(a.Version)+" AND action='recover_commit';")
			if err != nil {
				return protocol.NextAction{}, a, false, err
			}
			if held != "held" {
				n := protocol.NextAction{AttemptID: a.ID, Version: a.Version, Kind: "recover_commit", Disposition: "act"}
				return n, a, true, saveNextAction(ctx, g, n)
			}
		}
		n := protocol.NextAction{AttemptID: a.ID, Version: a.Version, Candidate: a.CandidateSHA, Kind: "inspect_execution", Disposition: "ask"}
		if err := saveNextAction(ctx, g, n); err != nil {
			return n, a, true, err
		}
		if err := syncOnce(ctx); err == nil {
			req := progressionRequest{Protocol: protocol.Version, AttemptID: a.ID, Version: a.Version, RecoveryHold: true}
			_, err = fetchProgression(ctx, p.enrolled, a, req)
			if err != nil && !errors.Is(err, statusError(403)) {
				return n, a, true, err
			}
		}
		return n, a, true, nil
	}
	if a.State == "waiting" && a.ReasonCode == protocol.RecoveredCandidate {
		n := protocol.NextAction{AttemptID: a.ID, Version: a.Version, Candidate: a.CandidateSHA, Kind: "resume_candidate", Disposition: "act"}
		return n, a, true, saveNextAction(ctx, g, n)
	}
	approver := ""
	question := a.State == "waiting" && a.ReasonCode == protocol.OwnerInputRequired && g.PortalDecisions && a.OwnerQuestion != ""
	if a.State == "waiting" && (a.ReasonCode == protocol.PlanReviewRequired || question) {
		approver = g.PlanApprover
	}
	if approver == "" && (a.State != "completed" || g.PublishTarget == "" && g.PlanApprover == "") {
		return protocol.NextAction{}, a, false, nil
	}
	req := progressionRequest{Protocol: protocol.Version, AttemptID: a.ID, Version: a.Version}
	if approver != "" {
		req.RequestPlan, req.ExpiresAt = true, &g.Expires
		if question {
			req.RequestPlan, req.Question = false, incidents.Redact(a.OwnerQuestion)
		} else if g.PortalDecisions {
			for _, artifact := range slices.Backward(a.Artifacts) {
				if artifact.Phase == "plan" {
					req.PlanSummary = incidents.Redact(artifact.Summary)
					break
				}
			}
		}
		if !question && approver != "owner" {
			req.Recipient = approver
		}
	}
	next, err := fetchProgression(ctx, p.enrolled, a, req)
	if err != nil {
		return next, a, true, err
	}
	if a.State == "completed" && a.Steward != nil && g.Steward && a.Steward.Target == g.PublishTarget && next.DecisionKind == "" {
		st := a.Steward
		req := progressionRequest{Protocol: protocol.Version, AttemptID: a.ID, Version: a.Version, ExpiresAt: &g.Expires, Correction: &protocol.CorrectionSubject{
			RootAttempt: st.RootAttempt, PullNumber: st.Pull, PreviousHead: st.PreviousHead, Target: st.Target,
		}}
		next, err = fetchProgression(ctx, p.enrolled, a, req)
		if err != nil {
			return next, a, true, err
		}
	}
	display := next
	display.Feedback = ""
	return display, a, true, saveNextAction(ctx, g, display)
}

func progressAttempt(ctx context.Context, p selectionProject, g unattendedGrant, a runner.Attempt, next protocol.NextAction) error {
	switch next.Kind {
	case "owner_input":
		if !g.PortalDecisions || next.Answer != "answered" || next.Question != incidents.Redact(a.OwnerQuestion) {
			return nil
		}
		return g.effect(ctx, a, next.Kind, false, func() error {
			fresh, err := hostedNextAction(ctx, p.enrolled, a, "")
			if err != nil {
				return err
			}
			if fresh.Kind != next.Kind || fresh.Answer != "answered" || fresh.Question != next.Question || fresh.Feedback == "" {
				return runner.ErrAuthorityStale
			}
			_, err = runner.AnswerUnattendedQuestion(ctx, a, g.authority(), fresh.Feedback)
			return err
		})
	case "recover_commit":
		return g.effect(ctx, a, next.Kind, true, func() error { _, err := runner.RecoverCommit(a.ID); return err })
	case "resume_candidate":
		return g.effect(ctx, a, next.Kind, false, func() error {
			_, err := runner.ContinueUnattendedPass(ctx, a, g.authority(), protocol.RecoveredCandidate)
			return err
		})
	case "review_plan":
		if next.Answer != "approve" {
			return nil
		}
		return g.effect(ctx, a, next.Kind, false, func() error {
			fresh, err := hostedNextAction(ctx, p.enrolled, a, "")
			if err != nil {
				return err
			}
			if fresh.Kind != "review_plan" || fresh.Answer != "approve" {
				return runner.ErrAuthorityStale
			}
			_, err = runner.ContinueUnattendedPass(ctx, a, g.authority(), protocol.PlanReviewRequired)
			return err
		})
	case "publish_draft":
		if next.Draft == nil || next.Draft.Target != g.PublishTarget || next.Draft.ApprovalVersion < 1 || a.Steward != nil {
			return nil
		}
		return g.effect(ctx, a, next.Kind+":"+strconv.FormatInt(next.Draft.ApprovalVersion, 10), true, func() error {
			return publishUnattended(ctx, p.enrolled, a, *next.Draft)
		})
	case "publish_correction":
		st, q := a.Steward, next.DecisionSubject
		if !g.Steward || st == nil || q == nil || next.Answer != "approve" || q.Target != g.PublishTarget ||
			q.RootAttempt != st.RootAttempt || q.PreviousHead != st.PreviousHead || q.PullNumber != st.Pull || q.Target != st.Target {
			return nil
		}
		return g.effect(ctx, a, next.Kind, true, func() error {
			confirm := func(ctx context.Context) error {
				fresh, err := hostedNextAction(ctx, p.enrolled, a, "")
				if err != nil {
					return err
				}
				if fresh.Kind != "publish_correction" || fresh.Answer != "approve" || fresh.DecisionSubject == nil || *fresh.DecisionSubject != *q {
					return runner.ErrAuthorityStale
				}
				return nil
			}
			if err := confirm(ctx); err != nil {
				return err
			}
			_, err := runner.PublishApprovedCorrection(ctx, a.ID, a.Version, a.CandidateSHA, q.Base, confirm)
			return err
		})
	case "wait_inspection":
		if next.Draft == nil || next.Draft.Target != g.PublishTarget || next.Draft.ApprovalVersion < 1 {
			return nil
		}
		req := draftRequest(a, g.PublishTarget)
		req.Operation, req.ApprovalVersion, req.Follow = "follow", next.Draft.ApprovalVersion, true
		return send(ctx, p.enrolled, "draft-delivery", req, &ShipReceipt{})
	case "prepare_signoff":
		return prepareSignoff(ctx, p.enrolled, g, a, next.Pull, next.PreparedEvidence != nil)
	}
	return nil
}

func publishUnattended(ctx context.Context, p profile, a runner.Attempt, approval protocol.DraftAction) error {
	req := draftRequest(a, approval.Target)
	req.Operation, req.ApprovalVersion = "status", approval.ApprovalVersion
	confirm := func(ctx context.Context) error {
		var r ShipReceipt
		if err := send(ctx, p, "draft-delivery", req, &r); err != nil {
			return err
		}
		if r.Status != "awaiting_publication" || r.ApprovalVersion != approval.ApprovalVersion {
			return runner.ErrAuthorityStale
		}
		return nil
	}
	if err := confirm(ctx); err != nil {
		return err
	}
	if _, err := runner.PublishApprovedDraft(ctx, a.ID, a.Version, a.CandidateSHA, approval.Target, confirm); err != nil {
		return err
	}
	req.Operation, req.Follow = "published", true
	return send(ctx, p, "draft-delivery", req, &ShipReceipt{})
}

func prepareSignoff(ctx context.Context, p profile, g unattendedGrant, a runner.Attempt, pull *protocol.ObservedPull, prepared bool) error {
	if pull == nil || pull.State != "OPEN" || pull.Head != a.CandidateSHA || pull.Target != g.PublishTarget {
		return nil
	}
	if g.Steward && (pull.Checks == "FAILURE" || pull.Checks == "FAILED" || pull.Review == "CHANGES_REQUESTED" || pull.Signal > 0) {
		var correction bool
		err := g.effect(ctx, a, fmt.Sprintf("steward:%d:%d", pull.Version, pull.Signal), false, func() error {
			next, err := runner.StewardUnattended(ctx, a, g.authority(), pull.Number)
			if errors.Is(err, runner.ErrStewardIdle) || errors.Is(err, runner.ErrStewardFinished) {
				return nil
			}
			correction = next.ID != a.ID
			return err
		})
		if err != nil || correction {
			return err
		}
	}
	if pull.Checks != "SUCCESS" || prepared {
		return nil
	}
	checks, err := runner.Checks(a.ID)
	if err != nil {
		return err
	}
	for _, c := range checks {
		if c.CandidateSHA == a.CandidateSHA && c.TargetSHA == pull.Base && c.CriteriaVersion == a.Issue.UpdatedAt && c.Status == "passed" && c.ExitCode != nil && *c.ExitCode == 0 {
			return reportPreparedEvidence(ctx, p, a, pull, c)
		}
	}
	key := fmt.Sprintf("combined_validation:%x", sha256.Sum256([]byte(pull.Base+" "+pull.Target)))
	return g.effect(ctx, a, key, false, func() error {
		ref, err := runner.RetainInspectedBase(ctx, a.Project, pull.Target, pull.Base)
		if err != nil {
			return err
		}
		c, err := runner.CheckCombinedAt(ctx, a.ID, ref, pull.Base)
		if err != nil {
			return err
		}
		return reportPreparedEvidence(ctx, p, a, pull, c)
	})
}

func reportPreparedEvidence(ctx context.Context, p profile, a runner.Attempt, pull *protocol.ObservedPull, c runner.IntegrationCheck) error {
	base := a.BaseSHA
	if a.Steward != nil {
		base = a.Steward.OriginalBase
	}
	_, reviewID, err := a.PassingReview(base)
	if err != nil {
		return err
	}
	combined := fmt.Sprintf("%x", sha256.Sum256([]byte(c.ID+" "+c.TargetSHA+" "+c.CandidateSHA+" "+c.CombinedSHA)))
	review := fmt.Sprintf("%x", sha256.Sum256([]byte(reviewID+" "+base+" "+a.CandidateSHA)))
	req := progressionRequest{Protocol: protocol.Version, AttemptID: a.ID, Version: a.Version, Prepared: &protocol.PreparedSignoff{
		PullNumber: pull.Number, InspectionVersion: pull.Version, Base: pull.Base, IndependentReview: review, CombinedValidation: combined,
	}}
	_, err = fetchProgression(ctx, p, a, req)
	return err
}

type unattendedProgress struct {
	attempt runner.Attempt
	next    protocol.NextAction
}

func scanProgress(ctx context.Context, p selectionProject, g unattendedGrant, attempts []runner.Summary, active map[int]bool) ([]unattendedProgress, error) {
	if g.PlanApprover != "" || g.PublishTarget != "" {
		if err := syncOnce(ctx); err != nil {
			return nil, err
		}
	}
	var ready []unattendedProgress
	seen := make(map[int]bool)
	// Local summaries are in insertion order. The latest correction owns
	// progression for the ticket, while the server keeps following its PR.
	for _, summary := range slices.Backward(attempts) {
		if active[summary.IssueNumber] || seen[summary.IssueNumber] {
			continue
		}
		seen[summary.IssueNumber] = true
		n, a, tracked, err := observeUnattended(ctx, p, g, summary)
		if err != nil {
			return nil, err
		}
		if tracked {
			ready = append(ready, unattendedProgress{a, n})
		}
	}
	return ready, nil
}

func (job unattendedProgress) pending(g unattendedGrant) bool {
	switch job.next.Kind {
	case "owner_input":
		return g.PortalDecisions && job.next.DecisionKind == "owner_question" && job.next.ExpiresAt != nil && time.Now().Before(*job.next.ExpiresAt)
	case "finished", "terminal", "device_revoked", "dispatch_paused", "hold", "inspect_revision", "inspect_execution", "wait_deployment", "deployment_handover":
		return false
	case "review_plan":
		return job.next.Answer != "hold" && (job.next.ExpiresAt == nil || time.Now().Before(*job.next.ExpiresAt))
	case "recover_commit", "resume_candidate":
		return true
	default:
		return g.PublishTarget != ""
	}
}

func (job unattendedProgress) runnable() bool {
	switch job.next.Kind {
	case "owner_input":
		return job.next.Answer == "answered"
	case "review_plan":
		return job.next.Answer == "approve"
	case "recover_commit", "resume_candidate", "publish_draft", "publish_correction", "wait_inspection", "prepare_signoff":
		return true
	}
	return false
}

// Observe reads the same projection as Work without enrolling or resuming an
// attempt. New checkpoints are visible once ordinary device sync reports them.
func observeProject(ctx context.Context, project string) ([]protocol.NextAction, error) {
	p, err := selectProject(project)
	if err != nil {
		return nil, err
	}
	summaries, err := runner.Summaries()
	if err != nil {
		return nil, err
	}
	actions := make([]protocol.NextAction, 0)
	seen := make(map[int]bool)
	for _, s := range slices.Backward(summaries) {
		if s.Workspace != p.enrolled.Enrollment.Workspace || !strings.EqualFold(s.Repository, p.repo) || seen[s.IssueNumber] {
			continue
		}
		seen[s.IssueNumber] = true
		a, err := runner.Get(s.ID)
		if err != nil {
			return nil, err
		}
		if a.LocalRunScope != nil && !validUnattendedScope(p, a.LocalRunScope) {
			continue
		}
		action, err := hostedNextAction(ctx, p.enrolled, a, "")
		if errors.Is(err, statusError(403)) {
			action = protocol.NextAction{AttemptID: a.ID, Version: a.Version, Candidate: a.CandidateSHA, Kind: "report_pending", Disposition: "wait"}
		} else if err != nil {
			return nil, err
		}
		action.Feedback = ""
		actions = append(actions, action)
	}
	return actions, nil
}
