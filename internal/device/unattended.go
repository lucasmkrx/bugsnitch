// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

const unattendedSchema = `CREATE TABLE IF NOT EXISTS unattended(
	project TEXT PRIMARY KEY, id TEXT NOT NULL, data TEXT NOT NULL, turns INTEGER NOT NULL DEFAULT 0,
	closed INTEGER NOT NULL DEFAULT 0, reason TEXT NOT NULL DEFAULT ''
);`

var errUnattendedUnsupported = errors.New("the service does not support this unattended selection request")

type unattendedScope struct {
	PortalDecisions bool                           `json:"portalDecisions,omitempty"`
	Scope           protocol.RunScope              `json:"scope"`
	Issues          map[int]protocol.IssueRevision `json:"issues"`
	Workspace       string                         `json:"workspace"`
	Repository      string                         `json:"repository"`
	Service         string                         `json:"service"`
	ProjectDigest   string                         `json:"projectDigest"`
	Duration        string                         `json:"duration"`
	Parallel        int                            `json:"parallel"`
	TurnBudget      int                            `json:"turnBudget"`
	Corrections     bool                           `json:"allowCorrections"`
	PlanApprover    string                         `json:"planApprover,omitempty"`
	PublishTarget   string                         `json:"publishTarget,omitempty"`
	Steward         bool                           `json:"steward,omitempty"`
}

type unattendedGrant struct {
	unattendedScope
	ID      string    `json:"id"`
	Expires time.Time `json:"expiresAt"`
}

type unattendedStatus struct {
	Grant     unattendedGrant `json:"grant"`
	TurnsUsed int             `json:"turnsUsed"`
	Closed    bool            `json:"closed"`
	Reason    string          `json:"reason"`
}

func LocalUnattended(ctx context.Context, args []string, out io.Writer) error {
	const usage = "use local-unattended {arm|run|observe|status|stop} --project PROJECT_ID"
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(out, usage)
		return err
	}
	fs := flag.NewFlagSet("local-unattended "+args[0], flag.ContinueOnError)
	project := fs.String("project", "", "assigned project ID")
	if args[0] == "arm" {
		return armUnattended(ctx, fs, project, args[1:], out)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *project == "" || fs.NArg() != 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "run":
		return runUnattended(ctx, *project, out)
	case "observe":
		actions, err := observeProject(ctx, *project)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(actions)
	case "status":
		status, err := readUnattended(ctx, *project)
		if err != nil {
			return err
		}
		_, attempts, err := unattendedAttempts(status.Grant)
		if err != nil {
			return err
		}
		actions, err := unattendedActions(ctx, status.Grant)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(struct {
			unattendedStatus
			Attempts    []runner.Summary      `json:"attempts"`
			NextActions []protocol.NextAction `json:"nextActions"`
		}{status, attempts, actions})
	case "stop":
		status, err := readUnattended(ctx, *project)
		if err != nil {
			return err
		}
		return updateUnattended(ctx, status.Grant, "Stopped by the local operator", true)
	default:
		return errors.New(usage)
	}
}

func armUnattended(ctx context.Context, fs *flag.FlagSet, project *string, args []string, out io.Writer) error {
	issues := fs.String("issues", "", "comma-separated issue numbers from the approved plan")
	duration := fs.Duration("for", 0, "grant lifetime, up to 24h")
	parallel := fs.Int("parallel", 1, "maximum simultaneous attempts")
	turns := fs.Int("turns", 0, "total model starts authorized by this grant")
	corrections := fs.Bool("allow-corrections", false, "allow the project's configured automatic corrections")
	approver := fs.String("plan-approver", "", "owner or a named approver ID for remote plan decisions")
	target := fs.String("publish-target", "", "follow owner-approved delivery to this exact target")
	steward := fs.Bool("steward", false, "prepare reviewed PR corrections for fresh publication approval")
	approve := fs.String("approve", "", "authorize the exact preview with this digest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	numbers, err := unattendedIssues(*issues)
	if err != nil {
		return err
	}
	if *project == "" || fs.NArg() != 0 || *duration < time.Minute || *duration > 24*time.Hour || *turns < 1 || *turns > 512 {
		return errors.New("provide a project, a duration from 1m to 24h, and a total turn budget from 1 to 512")
	}
	if (*target != "" && (!github.ValidBranch(*target) || strings.HasPrefix(*target, "bugsnitch/"))) || (*steward && *target == "") || len(*approver) > 100 {
		return errors.New("choose a valid publication target and plan approver; stewardship requires a target")
	}
	limits, err := runner.SetLocalLimits(0, 0)
	if err != nil {
		return err
	}
	if *parallel < 1 || *parallel > limits.Tickets {
		return fmt.Errorf("choose parallelism between 1 and the local ticket limit of %d", limits.Tickets)
	}
	p, err := selectProject(*project)
	if err != nil {
		return err
	}
	digest, err := runner.UnattendedProject(p.enrolled.Enrollment.Workspace, p.repo, p.path)
	if err != nil {
		return err
	}
	rec, err := p.unattendedNext(ctx, numbers)
	if err != nil {
		return err
	}
	if rec.Scope == nil {
		return errors.New(rec.Reason)
	}
	if !validUnattendedScope(p, rec.Scope) || !validBindings(numbers, rec.Bindings) {
		return errors.New("each selected ticket needs an open, canonical revision in the current approved plan")
	}
	scope := unattendedScope{
		Scope: *rec.Scope, Issues: rec.Bindings, Workspace: p.enrolled.Enrollment.Workspace,
		Repository: p.repo, Service: p.enrolled.Server, ProjectDigest: digest,
		Duration: duration.String(), Parallel: *parallel, TurnBudget: *turns, Corrections: *corrections,
		PlanApprover: *approver, PublishTarget: *target, Steward: *steward,
	}
	preview, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(preview)
	approvalDigest := hex.EncodeToString(hash[:])
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if *approve == "" {
		return enc.Encode(struct {
			unattendedScope
			Digest string `json:"approvalDigest"`
		}{scope, approvalDigest})
	}
	if *approve != approvalDigest {
		return errors.New("the approval digest differs from the current preview")
	}
	unlock, err := runner.LockUnattended(*project)
	if err != nil {
		return err
	}
	defer unlock()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	grant := unattendedGrant{scope, hex.EncodeToString(nonce[:]), time.Now().UTC().Add(*duration)}
	data, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	_, err = journalContext(ctx, unattendedSchema+"INSERT OR REPLACE INTO unattended(project,id,data) VALUES("+
		sqlValue(*project)+","+sqlValue(grant.ID)+","+sqlValue(string(data))+");")
	if err != nil {
		return err
	}
	return enc.Encode(grant)
}

func unattendedIssues(raw string) ([]int, error) {
	var numbers []int
	seen := make(map[int]bool)
	for _, part := range strings.Split(raw, ",") {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || number < 1 || seen[number] || len(numbers) == 32 {
			return nil, errors.New("choose one to 32 distinct positive issue numbers")
		}
		seen[number] = true
		numbers = append(numbers, number)
	}
	return numbers, nil
}

func validBindings(numbers []int, bindings map[int]protocol.IssueRevision) bool {
	if len(numbers) != len(bindings) {
		return false
	}
	for _, number := range numbers {
		issue := bindings[number]
		if issue.GitHubID < 1 || len(issue.Digest) != 64 || issue.Revision == "" {
			return false
		}
	}
	return true
}

func validUnattendedScope(p selectionProject, scope *protocol.RunScope) bool {
	return scope.Device == p.enrolled.Enrollment.DeviceID && scope.Project == p.id && scope.Plan != "" && scope.Policy > 0
}

func readUnattended(ctx context.Context, project string) (unattendedStatus, error) {
	var status unattendedStatus
	raw, err := journalContext(ctx, unattendedSchema+
		"SELECT json_object('grant',json(data),'turnsUsed',turns,'closed',json(CASE closed WHEN 1 THEN 'true' ELSE 'false' END),'reason',reason) FROM unattended WHERE project="+sqlValue(project)+";")
	if err != nil {
		return status, err
	}
	if raw == "" {
		return status, errors.New("no unattended grant for this project")
	}
	return status, json.Unmarshal([]byte(raw), &status)
}

func updateUnattended(ctx context.Context, grant unattendedGrant, reason string, close bool) error {
	closed := "0"
	if close {
		closed = "1"
	}
	_, err := journalContext(ctx, "UPDATE unattended SET reason="+sqlValue(reason)+",closed="+closed+
		" WHERE project="+sqlValue(grant.Scope.Project)+" AND id="+sqlValue(grant.ID)+" AND closed=0;")
	return err
}

func (grant unattendedGrant) takeTurn(ctx context.Context) error {
	current := fmt.Sprintf("project=%s AND id=%s AND closed=0 AND unixepoch('subsec')*1000<%d",
		sqlValue(grant.Scope.Project), sqlValue(grant.ID), grant.Expires.UnixMilli())
	outcome, err := journalContext(ctx, fmt.Sprintf(`BEGIN IMMEDIATE;
		UPDATE unattended SET turns=turns+1 WHERE %s AND turns<%d;
		SELECT CASE WHEN changes()=1 THEN 'taken'
			WHEN EXISTS(SELECT 1 FROM unattended WHERE %s) THEN 'budget' ELSE 'closed' END;
		COMMIT;`, current, grant.TurnBudget, current))
	if err != nil {
		return err
	}
	switch outcome {
	case "taken":
		return nil
	case "budget":
		return runner.ErrUnattendedBudget
	default:
		return runner.ErrAuthorityStale
	}
}

func unattendedAttempts(grant unattendedGrant) ([]int, []runner.Summary, error) {
	summaries, err := runner.Summaries()
	if err != nil {
		return nil, nil, err
	}
	var attempts []runner.Summary
	seen := make(map[int]bool)
	for _, a := range summaries {
		if a.Workspace == grant.Workspace && strings.EqualFold(a.Repository, grant.Repository) && grant.Issues[a.IssueNumber].GitHubID > 0 {
			attempts = append(attempts, a)
			seen[a.IssueNumber] = true
		}
	}
	var pending []int
	for n := range grant.Issues {
		if !seen[n] {
			pending = append(pending, n)
		}
	}
	return pending, attempts, nil
}

func (p selectionProject) unattendedNext(ctx context.Context, issues []int) (protocol.Recommendation, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var rec protocol.Recommendation
	err := send(ctx, p.enrolled, "next", protocol.NextRequest{Protocol: protocol.Version, Project: p.id, Issues: issues}, &rec)
	if errors.Is(err, statusError(http.StatusBadRequest)) {
		return rec, errUnattendedUnsupported
	}
	return rec, err
}

func runUnattended(ctx context.Context, project string, out io.Writer) error {
	unlock, err := runner.LockUnattended(project)
	if err != nil {
		return err
	}
	defer unlock()
	return superviseUnattended(ctx, project, out)
}

// Caller holds the project's exclusive supervisor lock.
func superviseUnattended(ctx context.Context, project string, out io.Writer) error {
	status, err := readUnattended(ctx, project)
	if err != nil {
		return err
	}
	grant := status.Grant
	if status.Closed {
		return errors.New(status.Reason)
	}
	if !time.Now().Before(grant.Expires) {
		return updateUnattended(ctx, grant, "Authorization expired", true)
	}
	if !grant.PortalDecisions && strings.TrimSpace(os.Getenv("BUGSNITCH_ANTHROPIC_API_KEY")) == "" {
		return errors.New("supply the runner's BUGSNITCH_ANTHROPIC_API_KEY before starting unattended work")
	}
	limits, err := runner.SetLocalLimits(0, 0)
	if err != nil {
		return err
	}
	parallel := min(grant.Parallel, limits.Tickets)
	if parallel < 1 || grant.Scope.Project != project {
		return errors.New("invalid unattended grant in the local journal")
	}
	run, cancel := context.WithDeadline(ctx, grant.Expires)
	defer cancel()
	p, err := selectProject(project)
	if err != nil {
		return err
	}
	wake := make(chan struct{}, 1)
	wakeDone := make(chan struct{})
	go func() {
		defer close(wakeDone)
		watchProgression(run, p.enrolled, project, wake)
	}()
	defer func() { cancel(); <-wakeDone }()
	type outcome struct {
		issue   int
		attempt runner.Attempt
		err     error
		started bool
	}
	results := make(chan outcome, parallel)
	active := make(map[int]bool)
	defer func() {
		cancel()
		for range len(active) {
			<-results
		}
	}()

	enc := json.NewEncoder(out)
	var lastReason string
	lastActions := make(map[string]string)
	update := func(reason string, closed bool) error {
		if !closed && reason == lastReason {
			return nil
		}
		lastReason = reason
		if err := updateUnattended(ctx, grant, reason, closed); err != nil {
			return err
		}
		return enc.Encode(map[string]any{"reason": reason, "closed": closed})
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var nextPoll time.Time
	for run.Err() == nil && time.Now().Before(grant.Expires) {
		status, err = readUnattended(run, project)
		if err != nil {
			if run.Err() != nil {
				break
			}
			return err
		}
		if status.Closed || status.Grant.ID != grant.ID {
			return nil
		}
		if len(active) < parallel && !time.Now().Before(nextPoll) {
			nextPoll = time.Now().Add(5 * time.Second)
			pending, attempts, err := unattendedAttempts(grant)
			if err != nil {
				return err
			}
			pending = slices.DeleteFunc(pending, func(number int) bool { return active[number] })
			p, err := selectProject(project)
			if err != nil {
				return update(err.Error(), true)
			}
			verifyProject := runner.UnattendedProject
			if grant.PortalDecisions {
				verifyProject = runner.PortalProject
			}
			digest, err := verifyProject(grant.Workspace, p.repo, p.path)
			if err != nil {
				return update(err.Error(), true)
			}
			if digest != grant.ProjectDigest || p.enrolled.Server != grant.Service || p.enrolled.Enrollment.Workspace != grant.Workspace || !validUnattendedScope(p, &grant.Scope) {
				return update("Device, service or local project configuration changed", true)
			}
			if len(pending) != 0 && status.TurnsUsed < grant.TurnBudget {
				rec, err := p.unattendedNext(run, pending)
				if run.Err() != nil {
					break
				}
				if err != nil {
					permanent := errors.Is(err, statusError(401)) || errors.Is(err, statusError(403)) || errors.Is(err, errUnattendedUnsupported)
					if e := update(err.Error(), permanent); e != nil || permanent {
						return errors.Join(err, e)
					}
				} else if reason := unattendedHold(grant, pending, rec); reason != "" {
					return update(reason, true)
				} else if rec.Selection == nil {
					if err = update(rec.Reason, false); err != nil {
						return err
					}
				} else {
					issue := runner.Issue(*rec.Selection)
					if err = update(fmt.Sprintf("Starting issue #%d", issue.Number), false); err != nil {
						return err
					}
					active[issue.Number] = true
					go func() {
						a, err := runner.StartUnattendedPass(run, grant.Workspace, issue, grant.Scope, grant.authority(), func(runner.Summary) {})
						results <- outcome{issue.Number, a, err, true}
					}()
					nextPoll = time.Time{}
					continue
				}
			}
			progression, observeErr := scanProgress(run, p, grant, attempts, active)
			if observeErr != nil {
				if run.Err() != nil {
					break
				}
				if err := update(observeErr.Error(), false); err != nil {
					return err
				}
			} else {
				if len(active) == 0 && !slices.ContainsFunc(progression, func(job unattendedProgress) bool { return job.pending(grant) }) {
					if len(pending) == 0 {
						return update("All selected tickets have recorded attempts. Inspect their outcomes with status.", true)
					}
					if status.TurnsUsed >= grant.TurnBudget {
						return update("Model-turn budget exhausted", true)
					}
				}
				for _, job := range progression {
					data, err := json.Marshal(job.next)
					if err != nil {
						return err
					}
					if lastActions[job.attempt.ID] != string(data) {
						if err := enc.Encode(job.next); err != nil {
							return err
						}
						lastActions[job.attempt.ID] = string(data)
					}
					if len(active) >= parallel {
						break
					}
					if active[job.attempt.IssueNumber] || !job.runnable() {
						continue
					}
					active[job.attempt.IssueNumber] = true
					go func() {
						err := progressAttempt(run, p, grant, job.attempt, job.next)
						results <- outcome{job.attempt.IssueNumber, job.attempt, err, false}
					}()
				}
			}
		}
		select {
		case <-run.Done():
		case <-ticker.C:
		case <-wake:
			nextPoll = time.Time{}
		case result := <-results:
			delete(active, result.issue)
			if !result.started && result.err == nil {
				continue
			}
			event := map[string]any{"issue": result.issue, "attempt": result.attempt.ID, "state": result.attempt.State, "reason": result.attempt.Message}
			if result.err != nil {
				event["reason"] = result.err.Error()
			}
			if err := enc.Encode(event); err != nil {
				return err
			}
			if result.err == nil && result.started {
				nextPoll = time.Time{}
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return update("Authorization expired", true)
}

func unattendedHold(grant unattendedGrant, pending []int, rec protocol.Recommendation) string {
	if rec.Hold {
		return rec.Reason
	}
	if rec.Scope == nil || *rec.Scope != grant.Scope {
		return "The approved plan or project policy changed"
	}
	for _, number := range pending {
		if rec.Bindings[number] != grant.Issues[number] {
			return fmt.Sprintf("Issue #%d no longer matches the authorized revision", number)
		}
	}
	if issue := rec.Selection; issue != nil {
		binding := protocol.IssueRevision{GitHubID: issue.GitHubID, Digest: issue.RequirementsDigest, Revision: issue.UpdatedAt}
		if !slices.Contains(pending, issue.Number) || !strings.EqualFold(issue.Repository, grant.Repository) || binding != grant.Issues[issue.Number] {
			return "The recommendation differs from the authorized ticket or repository"
		}
	}
	return ""
}
