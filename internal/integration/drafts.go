// SPDX-License-Identifier: MPL-2.0
package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

type DraftRequest struct {
	Target        string `json:"target"`
	Version       int64  `json:"version"`
	PolicyVersion int64  `json:"policyVersion"`
	Summary       string `json:"summary,omitempty"`
	Behavior      string `json:"behavior,omitempty"`
}
type Draft struct {
	ProjectID   string    `json:"projectId"`
	AttemptID   string    `json:"attemptId"`
	IssueNumber int64     `json:"issueNumber"`
	Number      *int64    `json:"number,omitempty"`
	Candidate   string    `json:"candidateSha"`
	Base        string    `json:"baseSha"`
	Target      string    `json:"target"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

var attemptIdentity = regexp.MustCompile(`^[a-f0-9]{32}$`)

const draftCols = "project_id,attempt_id,issue_number,number,candidate_sha,base_sha,target,status,reason,updated_at"

func scanDraft(row scanner) (Draft, error) {
	var d Draft
	err := row.Scan(&d.ProjectID, &d.AttemptID, &d.IssueNumber, &d.Number, &d.Candidate, &d.Base, &d.Target, &d.Status, &d.Reason, &d.UpdatedAt)
	return d, err
}
func (s Store) Drafts(ctx context.Context, w string) ([]Draft, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+draftCols+" FROM draft_pr_intents WHERE workspace_id=$1 ORDER BY updated_at DESC,attempt_id LIMIT 500", w)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Draft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

type candidate struct {
	repo, head, base, issueTime             string
	install, repoID, issue, version, policy int64
}

func inspectCandidate(ctx context.Context, tx *sql.Tx, w, p, id string) (candidate, error) {
	var c candidate
	var planID string
	var stages []byte
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT p.repository,m.installation_id,m.repository_id,a.candidate_sha,a.base_sha,a.issue_number,a.issue_updated_at,a.version,p.policy_version,a.evidence,coalesce((SELECT plan_id FROM admission WHERE workspace_id=a.workspace_id AND attempt_id=a.id),''),
	m.active AND NOT w.stopped AND NOT p.paused AND p.allow_draft_pr AND NOT d.revoked AND a.state='completed' AND a.phase='wrap_up'
	FROM attempt_reports a JOIN projects p ON (p.id,p.workspace_id)=(a.project_id,a.workspace_id)
	JOIN workspaces w ON w.id=a.workspace_id
	JOIN devices d ON (d.id,d.workspace_id)=(a.device_id,a.workspace_id)
	JOIN github_mappings m ON (m.project_id,m.workspace_id)=(p.id,p.workspace_id)
	WHERE p.workspace_id=$1 AND p.id=$2 AND a.id=$3 FOR SHARE OF w,p,m,d,a`, w, p, id).Scan(&c.repo, &c.install, &c.repoID, &c.head, &c.base, &c.issue, &c.issueTime, &c.version, &c.policy, &stages, &planID, &allowed)
	if err != nil {
		return c, err
	}
	if !allowed || c.head == "" || c.head == c.base || !stageEvidence(stages, c.head) {
		return c, ErrConflict
	}
	if _, err = time.Parse(time.RFC3339, c.issueTime); err != nil {
		return c, ErrConflict
	}
	if err := planning.CheckIssue(ctx, tx, w, p, planID, int(c.issue), false); err != nil {
		if errors.Is(err, planning.ErrIssueDrift) {
			return c, ErrConflict
		}
		return c, err
	}
	return c, nil
}
func stageEvidence(stages []byte, head string) bool {
	var evidence []protocol.StageEvidence
	if json.Unmarshal(stages, &evidence) != nil {
		return false
	}
	valid, review := false, false
	for _, e := range evidence {
		switch e.Phase {
		case "validate":
			valid = e.CandidateSHA == head && e.ExitCode != nil && *e.ExitCode == 0
			review = false
		case "review":
			review = valid && e.CandidateSHA == head && e.ImportantFindings == 0
		}
	}
	return valid && review
}
func (s Store) RequestDraft(ctx context.Context, w, p, id string, r DraftRequest) (Draft, error) {
	var empty Draft
	if !attemptIdentity.MatchString(id) || !github.ValidBranch(r.Target) || strings.HasPrefix(r.Target, "bugsnitch/") || r.Version < 1 || r.PolicyVersion < 1 || len(r.Summary) > 2048 || len(r.Behavior) > 2048 {
		return empty, ErrInvalid
	}
	if s.App == nil {
		return empty, ErrUnavailable
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	c, err := inspectCandidate(ctx, tx, w, p, id)
	if err != nil {
		return empty, err
	}
	if c.version != r.Version || c.policy != r.PolicyVersion {
		return empty, ErrConflict
	}
	r.Summary = incidents.Redact(r.Summary)
	r.Behavior = incidents.Redact(r.Behavior)
	var nonce [16]byte
	rand.Read(nonce[:])
	result, err := tx.ExecContext(ctx, `INSERT INTO draft_pr_intents(workspace_id,project_id,attempt_id,report_version,policy_version,installation_id,repository_id,issue_number,issue_updated_at,candidate_sha,base_sha,target,marker,status,summary,behavior)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'queued',$14,$15) ON CONFLICT DO NOTHING`, w, p, id, r.Version, r.PolicyVersion, c.install, c.repoID, c.issue, c.issueTime, c.head, c.base, r.Target, hex.EncodeToString(nonce[:]), r.Summary, r.Behavior)
	if err != nil {
		return empty, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return empty, err
	}
	d, err := scanDraft(tx.QueryRowContext(ctx, "SELECT "+draftCols+" FROM draft_pr_intents WHERE workspace_id=$1 AND project_id=$2 AND attempt_id=$3 AND report_version=$4 AND policy_version=$5 AND target=$6 AND summary=$7 AND behavior=$8", w, p, id, r.Version, r.PolicyVersion, r.Target, r.Summary, r.Behavior))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, ErrConflict
	}
	if err != nil {
		return empty, err
	}
	if count == 1 {
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Reviewed candidate queued for draft publication", PolicyVersion: r.PolicyVersion, SubjectVersion: r.Version, EvidenceRef: c.head})
		if err = draftEvent(ctx, tx, w, p, id, "draft.requested"); err != nil {
			return empty, err
		}
	}
	return d, tx.Commit()
}
func draftEvent(ctx context.Context, tx *sql.Tx, w, p, id, kind string) error {
	return audit.Record(ctx, tx, w, p, kind, id)
}

type job struct {
	w, p, id, target, marker, state, name, summary, behavior string
	c                                                        candidate
	tries                                                    int
}

func (j job) branch() string { return "bugsnitch/" + j.id }
func (j job) mark() string   { return "<!-- bugsnitch:draft:" + j.marker + " -->" }
func (j job) finish(ctx context.Context, tx *sql.Tx, status, reason string, number *int64, err error) error {
	delay := time.Minute << min(j.tries, 6)
	var failure *github.APIError
	if errors.As(err, &failure) && failure.RetryAfter > delay {
		delay = failure.RetryAfter
	}
	if j.tries >= 7 && status != "created" {
		status = "blocked"
		reason += ". Retry limit reached; inspect the repository"
	}
	_, err = tx.ExecContext(ctx, `UPDATE draft_pr_intents SET status=$3,reason=$4,number=$5,tries=least(8,tries+1),next_run=now()+($6::int * interval '1 second'),updated_at=now()
	WHERE workspace_id=$1 AND attempt_id=$2`, j.w, j.id, status, reason, number, int(delay.Seconds()))
	if err != nil {
		return err
	}
	if status != j.state {
		ctx = audit.WithDetails(ctx, audit.Details{Reason: reason, PolicyVersion: j.c.policy, SubjectVersion: j.c.version, EvidenceRef: j.c.head})
		if err = draftEvent(ctx, tx, j.w, j.p, j.id, "draft."+status); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (j job) check(ctx context.Context, tx *sql.Tx) error {
	c, err := inspectCandidate(ctx, tx, j.w, j.p, j.id)
	if err != nil {
		return err
	}
	if c.version != j.c.version || c.policy != j.c.policy || c.repo != j.name || c.repoID != j.c.repoID || c.install != j.c.install || c.head != j.c.head || c.base != j.c.base || c.issue != j.c.issue || c.issueTime != j.c.issueTime {
		return ErrConflict
	}
	return nil
}
func (s Store) RunDrafts(ctx context.Context) {
	ctx = audit.WithActor(ctx, audit.Service, "draft-pr-worker")
	if s.App == nil {
		return
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		task, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := s.syncDraft(task)
		cancel()
		if err != nil && !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			slog.Warn("draft synchronization deferred")
		}
	}
}
func (s Store) syncDraft(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var j job
	err = tx.QueryRowContext(ctx, `SELECT x.workspace_id,x.project_id,x.attempt_id,x.target,x.marker,x.status,x.tries,p.repository,x.summary,x.behavior,
	x.report_version,x.policy_version,x.installation_id,x.repository_id,x.candidate_sha,x.base_sha,x.issue_number,x.issue_updated_at
	FROM draft_pr_intents x JOIN projects p ON (p.workspace_id,p.id)=(x.workspace_id,x.project_id)
	JOIN github_mappings m USING(installation_id,repository_id)
	WHERE m.workspace_id=x.workspace_id AND m.project_id=x.project_id AND m.active AND x.status IN('queued','reconciling') AND x.next_run<=now()
	ORDER BY x.next_run,x.attempt_id FOR UPDATE OF x SKIP LOCKED LIMIT 1`).Scan(&j.w, &j.p, &j.id, &j.target, &j.marker, &j.state, &j.tries, &j.name, &j.summary, &j.behavior, &j.c.version, &j.c.policy, &j.c.install, &j.c.repoID, &j.c.head, &j.c.base, &j.c.issue, &j.c.issueTime)
	if err != nil {
		return err
	}
	repo, err := s.App.DraftRepository(ctx, j.c.install, j.c.repoID, j.name)
	if err != nil {
		return j.finish(ctx, tx, j.state, "GitHub installation unavailable", nil, err)
	}
	pr, err := repo.Find(ctx, j.branch(), j.target, j.mark())
	if errors.Is(err, github.ErrSubjectChanged) {
		return j.finish(ctx, tx, "blocked", "A different pull request uses this branch or marker", nil, err)
	}
	if err != nil {
		return j.finish(ctx, tx, j.state, "Pull-request lookup unavailable", nil, err)
	}
	if pr != nil {
		return j.record(ctx, tx, pr)
	}
	if j.state == "reconciling" {
		return j.finish(ctx, tx, j.state, "Uncertain create. Waiting for an App-authored PR with the recorded marker", nil, nil)
	}
	if err = j.check(ctx, tx); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, sql.ErrNoRows) {
			return j.finish(ctx, tx, "blocked", "Attempt evidence, policy or device authorization changed", nil, err)
		}
		return err
	}
	// Commit the create intent before any external mutation. Only this goroutine may send it.
	_, err = tx.ExecContext(ctx, `UPDATE draft_pr_intents SET status='reconciling',reason='Confirming draft creation',next_run=now()+interval '2 minutes',updated_at=now() WHERE workspace_id=$1 AND attempt_id=$2`, j.w, j.id)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.sendDraft(ctx, j, repo)
}
func (j job) record(ctx context.Context, tx *sql.Tx, pr *github.DraftPull) error {
	reason := ""
	if pr.Head.Sha != j.c.head || pr.Base.Sha != j.c.base {
		reason = "Published revisions changed. Refresh the inspection before review"
	}
	return j.finish(ctx, tx, "created", reason, &pr.Number, nil)
}
func (s Store) sendDraft(ctx context.Context, j job, repo *github.DraftRepository) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM draft_pr_intents WHERE workspace_id=$1 AND attempt_id=$2 FOR UPDATE`, j.w, j.id).Scan(&state); err != nil {
		return err
	}
	if state != "reconciling" {
		return nil
	}
	if err = j.check(ctx, tx); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, sql.ErrNoRows) {
			return j.finish(ctx, tx, "blocked", "Policy or evidence changed before publication", nil, err)
		}
		return err
	}
	if err = repo.Check(ctx, j.branch(), j.c.head, j.target, j.c.base, j.c.issue, j.c.issueTime); err != nil {
		if errors.Is(err, github.ErrSubjectChanged) {
			return j.finish(ctx, tx, "blocked", "Issue, target or candidate changed; review current evidence", nil, err)
		}
		return j.finish(ctx, tx, "queued", "Could not confirm published branch, target and open issue", nil, err)
	}
	title := fmt.Sprintf("Changes for issue #%d", j.c.issue)
	quote := func(s string) string { return "    " + strings.ReplaceAll(incidents.Redact(s), "\n", "\n    ") }
	if j.summary == "" {
		j.summary = "See the candidate diff and the local attempt's change summary."
	}
	if j.behavior == "" {
		j.behavior = "See the canonical issue and the local acceptance record."
	}
	body := fmt.Sprintf("## Problem\n\nTracks #%d.\n\n## Changes\n\n%s\n\n## Resulting behavior\n\n%s\n\n## Verification\n\nRunner validation exited 0 at %s. Independent review at that revision recorded zero important findings. Base revision %s.\n\n## Limitations\n\nCombined integration and environment verification remain pending.\n\n%s", j.c.issue, quote(j.summary), quote(j.behavior), j.c.head, j.c.base, j.mark())
	pr, err := repo.Create(ctx, j.branch(), j.target, title, body)
	if err != nil {
		var failure *github.APIError
		if errors.As(err, &failure) {
			switch failure.Status {
			case 401, 403, 429:
				return j.finish(ctx, tx, "queued", "GitHub denied or rate-limited creation", nil, err)
			case 404, 410, 422:
				return j.finish(ctx, tx, "blocked", "GitHub declined this draft. Check existing pull requests and repository settings", nil, err)
			}
		}
		return j.finish(ctx, tx, "reconciling", "Create response uncertain. Checking repository for the matching PR", nil, err)
	}
	return j.record(ctx, tx, &pr)
}
