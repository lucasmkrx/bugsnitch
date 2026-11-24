// SPDX-License-Identifier: MPL-2.0
package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
)

type QueueRequest struct {
	SignOff
	Mode        string `json:"mode"`
	Environment string `json:"environment"`
}
type QueueDecision struct {
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	GroupSHA string `json:"groupSha,omitempty"`
}

type permit struct {
	w, p                                                                  string
	n, v, policy, plan, installation, repo, issue                         int64
	head, base, target, criteria, rules, env, attempt, name, state, group string
}

func (a permit) subjectValid(o github.PullObservation) bool {
	return o.State == "OPEN" && o.IssueState == "OPEN" && !o.Draft && o.Head == a.head && o.Base == a.base && o.Target == a.target && o.Criteria == a.criteria &&
		o.CheckCount > 0 && o.Checks == "SUCCESS" && o.Review == "APPROVED" && o.Queue != nil && o.Queue.OwnerType == "Organization" &&
		o.Queue.Present && o.Queue.Rules != nil && o.Queue.Rules.Digest == a.rules && o.Queue.Rules.QueueRequired
}

func (a permit) checkContext(ctx context.Context, tx *sql.Tx) error {
	c, err := inspectCandidate(ctx, tx, a.w, a.p, a.attempt)
	if err != nil {
		return err
	}
	if c.head != a.head || c.issue != a.issue || c.install != a.installation || c.repoID != a.repo || c.policy != a.policy {
		return ErrConflict
	}
	var ready bool
	err = tx.QueryRowContext(ctx, `SELECT NOT w.stopped AND NOT p.paused AND p.policy_version=$3 AND
	 coalesce((SELECT max(version) FROM plan_versions WHERE workspace_id=$1 AND project_id=$2 AND approved_at IS NOT NULL),0)=$4
	 AND NOT EXISTS(SELECT FROM release_queue_holds WHERE workspace_id=$1 AND project_id=$2 AND pull_number=$5)
	 AND NOT EXISTS(SELECT FROM decisions WHERE workspace_id=$1 AND project_id=$2 AND answer IS NULL AND subject_version=p.version AND expires_at>now())
	 AND EXISTS(SELECT FROM pull_evidence e WHERE e.workspace_id=$1 AND e.project_id=$2 AND e.number=$5 AND e.version=$6 AND e.kind='sign_off'
	    AND (e.source='owner:'||w.account_id OR EXISTS(SELECT FROM workspace_members m WHERE m.workspace_id=$1 AND e.source='approver:'||m.id
	       AND m.role='approver' AND m.revoked_at IS NULL AND (m.project_id IS NULL OR m.project_id=$2))))
	 AND NOT EXISTS(SELECT FROM deployment_holds h JOIN deployment_monitors m ON m.id=h.monitor_id WHERE m.workspace_id=$1 AND m.project_id=$2 AND h.cleared_at IS NULL)
	 FROM workspaces w JOIN projects p ON p.workspace_id=w.id WHERE w.id=$1 AND p.id=$2 FOR SHARE OF w,p`, a.w, a.p, a.policy, a.plan, a.n, a.v).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return ErrConflict
	}
	// Admission records may name a plan. Require the same active plan, a still
	// eligible issue and every dependency's current integration evidence.
	if a.plan > 0 {
		var planID string
		var deps []byte
		err = tx.QueryRowContext(ctx, `SELECT v.id,coalesce(t.dependencies,'[]'::jsonb) FROM admission ad JOIN plan_versions v ON v.id=ad.plan_id
	      CROSS JOIN LATERAL jsonb_to_recordset(v.document->'tickets') AS t(number bigint,dependencies jsonb)
	      WHERE ad.workspace_id=$1 AND ad.attempt_id=$2 AND v.project_id=$3 AND v.version=$4 AND v.approved_at IS NOT NULL AND t.number=$5 FOR SHARE OF v,ad`, a.w, a.attempt, a.p, a.plan, a.issue).Scan(&planID, &deps)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		var dependencies []int
		if err = json.Unmarshal(deps, &dependencies); err != nil {
			return err
		}
		for _, n := range dependencies {
			var has bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT FROM prerequisite_evidence WHERE workspace_id=$1 AND project_id=$2 AND plan_id=$3 AND issue_number=$4)`, a.w, a.p, planID, n).Scan(&has); err != nil {
				return err
			}
			if !has {
				return ErrConflict
			}
			if err = planning.CheckIssue(ctx, tx, a.w, a.p, planID, n, true); errors.Is(err, planning.ErrIssueDrift) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

const queuePermit = `SELECT a.version,a.policy_version,a.plan_version,a.installation_id,a.repository_id,a.issue_number,a.head,a.base,a.target,a.criteria,
 a.rules_digest,a.environment,a.attempt_id,p.repository,a.state,a.group_sha,a.expires_at
 FROM pull_queue_permits a JOIN projects p ON (p.workspace_id,p.id)=(a.workspace_id,a.project_id)

 WHERE a.workspace_id=$1 AND a.project_id=$2 AND a.number=$3`

func (s Store) Queue(ctx context.Context, w, p string, n int64, r QueueRequest) (QueueDecision, error) {
	actor, id := audit.Actor(ctx)
	if actor != "owner" || id == "" || n < 1 || r.Version < 1 || !commitSHA.MatchString(r.Head) || !commitSHA.MatchString(r.Base) || !github.ValidBranch(r.Target) ||
		(r.Mode != "observe" && r.Mode != "enforce" && r.Mode != "revoke" && r.Mode != "renew") || !handoverLabel(r.Environment, 80) {
		return QueueDecision{}, ErrInvalid
	}
	if r.Mode == "revoke" {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return QueueDecision{}, err
		}
		defer tx.Rollback()
		var state string
		err = tx.QueryRowContext(ctx, `SELECT state FROM pull_queue_permits WHERE (workspace_id,project_id,number,version,head,base,target,environment)=($1,$2,$3,$4,$5,$6,$7,$8) FOR UPDATE`, w, p, n, r.Version, r.Head, r.Base, r.Target, r.Environment).Scan(&state)
		if err != nil {
			return QueueDecision{}, err
		}
		switch state {
		case "revoked":
			return QueueDecision{State: "revoked"}, nil
		case "requested", "submitting", "awaiting_queue", "queued", "checking":
		default:
			return QueueDecision{}, ErrConflict
		}
		err = (permit{w: w, p: p, n: n, v: r.Version, head: r.Head}).record(ctx, tx, "revoked", "Owner revoked queue authorization", "")
		return QueueDecision{State: "revoked"}, err
	}
	if r.Mode == "renew" {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return QueueDecision{}, err
		}
		defer tx.Rollback()
		var current int64
		err = tx.QueryRowContext(ctx, `SELECT o.version FROM pull_observations o JOIN pull_queue_permits a
		 ON (a.workspace_id,a.project_id,a.number,a.version)=(o.workspace_id,o.project_id,o.number,o.version)
		 WHERE (a.workspace_id,a.project_id,a.number,a.version,a.head,a.base,a.target,a.environment)=($1,$2,$3,$4,$5,$6,$7,$8)
		 AND a.state IN ('revoked','held') AND o.snapshot->>'state'='OPEN' FOR UPDATE OF o,a`, w, p, n, r.Version, r.Head, r.Base, r.Target, r.Environment).Scan(&current)
		if err != nil {
			return QueueDecision{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE pull_observations SET version=version+1 WHERE workspace_id=$1 AND project_id=$2 AND number=$3 AND version=$4`, w, p, n, current); err != nil {
			return QueueDecision{}, err
		}
		ctx = audit.WithDetails(ctx, audit.Details{SubjectVersion: current + 1, EvidenceRef: r.Head, Reason: "Owner started a fresh review after a stopped queue delivery"})
		if err = audit.Record(ctx, tx, w, p, "pull.queue_review_renewed", strconv.FormatInt(n, 10)); err != nil {
			return QueueDecision{}, err
		}
		return QueueDecision{State: "review_required"}, tx.Commit()
	}
	if r.Mode == "enforce" {
		var prior QueueDecision
		err := s.DB.QueryRowContext(ctx, `SELECT state,reason,group_sha FROM pull_queue_permits WHERE workspace_id=$1 AND project_id=$2 AND number=$3 AND version=$4 AND head=$5 AND base=$6 AND target=$7 AND environment=$8`, w, p, n, r.Version, r.Head, r.Base, r.Target, r.Environment).Scan(&prior.State, &prior.Reason, &prior.GroupSHA)
		if err == nil {
			return prior, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return prior, err
		}
	}
	var issue int64
	if err := s.DB.QueryRowContext(ctx, "SELECT issue_number FROM pull_observations WHERE workspace_id=$1 AND project_id=$2 AND number=$3", w, p, n).Scan(&issue); err != nil {
		return QueueDecision{}, err
	}
	o, err := s.Refresh(ctx, w, p, n, issue, r.Version)
	if err != nil {
		return QueueDecision{}, err
	}
	hold := func(reason string) (QueueDecision, error) {
		if r.Mode == "enforce" {
			return QueueDecision{}, ErrConflict
		}
		return QueueDecision{State: "hold", Reason: reason}, nil
	}
	if !r.ready(o) || o.SignOff == nil || o.Queue == nil || o.Queue.Rules == nil {
		return hold("Current sign-off, review, validation and queue protections are required")
	}
	a := permit{w: w, p: p, n: n, v: r.Version, policy: o.PolicyVersion, plan: o.PlanVersion, issue: issue, head: o.Head, base: o.Base, target: o.Target, criteria: o.Criteria, rules: o.Queue.Rules.Digest, env: r.Environment}
	if !a.subjectValid(o.PullObservation) {
		return hold("The approved subject or active target rules changed")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return QueueDecision{}, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT lineage.id,d.installation_id,d.repository_id,p.repository FROM draft_pr_intents d
	  JOIN LATERAL (WITH RECURSIVE chain(id,head,depth) AS (
	      SELECT d.attempt_id,d.candidate_sha,0 UNION ALL
	      SELECT a.id,a.candidate_sha,prior.depth+1 FROM attempt_reports a JOIN chain prior ON prior.head=a.base_sha
	      WHERE prior.depth<2 AND a.workspace_id=d.workspace_id AND a.project_id=d.project_id AND a.issue_number=d.issue_number
	        AND a.state='completed' AND a.phase='wrap_up' AND a.candidate_sha<>a.base_sha
	  ) SELECT id FROM chain WHERE head=$5 ORDER BY depth,id LIMIT 1) lineage ON true
	  JOIN projects p ON (p.workspace_id,p.id)=(d.workspace_id,d.project_id) JOIN pull_observations o ON (o.workspace_id,o.project_id,o.number)=(d.workspace_id,d.project_id,d.number)
	  WHERE d.workspace_id=$1 AND d.project_id=$2 AND d.number=$3 AND o.version=$4 AND o.snapshot->>'base'=$6 AND d.target=$7 AND d.status='created'
	  AND EXISTS(SELECT FROM pull_evidence e WHERE (e.workspace_id,e.project_id,e.number,e.version)=(o.workspace_id,o.project_id,o.number,o.version) AND e.kind='sign_off')
	  FOR SHARE OF o,d,p`, w, p, n, r.Version, a.head, a.base, a.target).Scan(&a.attempt, &a.installation, &a.repo, &a.name)
	if errors.Is(err, sql.ErrNoRows) {
		return hold("The PR must be the published, reviewed Bugsnitch candidate at the approved target")
	}
	if err != nil {
		return QueueDecision{}, err
	}
	if err = a.checkContext(ctx, tx); errors.Is(err, ErrConflict) {
		return hold("Project, deployment or candidate evidence requires attention")
	}
	if err != nil {
		return QueueDecision{}, err
	}
	if !s.App.QueueEnabled(a.repo) {
		return hold("Enable this numeric repository ID for protected queue writes after exercising the configured repository rules")
	}
	repo, err := s.App.QueueRepository(ctx, a.installation, a.repo, a.name, false)
	if err != nil {
		return QueueDecision{}, ErrUnavailable
	}
	defer repo.Close()
	current, err := repo.QueueSubject(ctx, n, a.target)
	if err != nil {
		return QueueDecision{}, ErrUnavailable
	}
	if !current.Guarded || current.Head != a.head || current.Base != a.base || current.Target != a.target {
		return hold("Configure an enforced queue with an App-bound authorization check and independent CI on its combined commits")
	}
	if current.InQueue {
		return hold("Remove the existing queue entry and authorize this revision before queue admission so its signed group event is captured")
	}
	if r.Mode == "observe" {
		if !current.CanEnqueue {
			return QueueDecision{State: "eligible", Reason: "Bugsnitch can authorize the reviewed head and verify the protected group. Add this PR to its native GitHub queue after the head check passes."}, nil
		}
		return QueueDecision{State: "eligible"}, nil
	}
	var state string
	err = tx.QueryRowContext(ctx, `INSERT INTO pull_queue_permits (workspace_id,project_id,number,version,policy_version,plan_version,installation_id,repository_id,head,base,target,criteria,rules_digest,issue_number,actor_id,environment,attempt_id,state)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'requested') ON CONFLICT DO NOTHING RETURNING state`, w, p, n, r.Version, a.policy, a.plan, a.installation, a.repo, a.head, a.base, a.target, a.criteria, a.rules, issue, id, a.env, a.attempt).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueDecision{}, ErrConflict
	}
	if err != nil {
		return QueueDecision{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE pull_observations SET watching=true,next_watch=now() WHERE workspace_id=$1 AND project_id=$2 AND number=$3`, w, p, n); err != nil {
		return QueueDecision{}, err
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: "Owner approved protected queue and deployment effects for " + r.Environment, SubjectVersion: r.Version, PolicyVersion: a.policy, EvidenceRef: a.head})
	if err = audit.Record(ctx, tx, w, p, "pull.queue_authorized", strconv.FormatInt(n, 10)); err != nil {
		return QueueDecision{}, err
	}
	return QueueDecision{State: state}, tx.Commit()
}

func (a permit) record(ctx context.Context, tx *sql.Tx, state, reason, group string) error {
	_, err := tx.ExecContext(ctx, `UPDATE pull_queue_permits SET state=$5,reason=$6,group_sha=$7 WHERE workspace_id=$1 AND project_id=$2 AND number=$3 AND version=$4`, a.w, a.p, a.n, a.v, state, reason, group)
	if err != nil {
		return err
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: reason, SubjectVersion: a.v, PolicyVersion: a.policy, EvidenceRef: a.head})
	if err = audit.Record(ctx, tx, a.w, a.p, "pull.queue_"+state, strconv.FormatInt(a.n, 10)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s Store) advanceQueue(ctx context.Context, w, p string, n int64, o Pull) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a := permit{w: w, p: p, n: n}
	var deadline time.Time
	err = tx.QueryRowContext(ctx, queuePermit+` AND a.state IN ('requested','submitting','awaiting_queue','queued','checking') FOR UPDATE OF a`, w, p, n).Scan(&a.v, &a.policy, &a.plan, &a.installation, &a.repo, &a.issue, &a.head, &a.base, &a.target, &a.criteria, &a.rules, &a.env, &a.attempt, &a.name, &a.state, &a.group, &deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !deadline.After(time.Now()) || !a.subjectValid(o.PullObservation) {
		return a.record(ctx, tx, "held", "Queue authorization expired or its reviewed subject changed", a.group)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err = a.checkContext(ctx, tx); errors.Is(err, ErrConflict) || errors.Is(err, sql.ErrNoRows) {
		return a.record(ctx, tx, "held", "Project, provider, deployment or candidate authority changed", a.group)
	}
	if err != nil {
		return err
	}
	repo, err := s.App.QueueRepository(ctx, a.installation, a.repo, a.name, true)
	if err != nil {
		return err
	}
	defer repo.Close()
	subject, err := repo.QueueSubject(ctx, n, a.target)
	if errors.Is(err, github.ErrSubjectChanged) {
		return a.record(ctx, tx, "held", "GitHub pull request changed repository or target", a.group)
	}
	if err != nil {
		return err
	}
	if !subject.Guarded || subject.Head != a.head || subject.Base != a.base || subject.Target != a.target {
		return a.record(ctx, tx, "held", "GitHub target, candidate or required protections changed", a.group)
	}
	identity := func(sha string) string {
		return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d/%d/%d/%s/%s/%s/%s", a.repo, a.n, a.v, a.target, a.base, a.head, sha))))
	}
	if !subject.InQueue {
		switch a.state {
		case "requested":
			// Commit intent before external writes. A restarted worker may
			// observe the queue; it never resubmits an uncertain enqueue.
			if err = a.record(ctx, tx, "submitting", "Queue submission reserved", ""); err != nil {
				return err
			}
			return s.submitQueue(ctx, a, identity(a.head))
		case "awaiting_queue":
			return nil
		case "submitting", "queued", "checking":
			return a.record(ctx, tx, "held", "Queue entry is absent after submission; inspect GitHub before a fresh decision", a.group)
		}
	}
	if a.state != "queued" && a.state != "checking" {
		return a.record(ctx, tx, "queued", "GitHub confirmed queue membership", "")
	}
	rows, err := tx.QueryContext(ctx, `SELECT head,head_ref FROM github_merge_groups WHERE workspace_id=$1 AND project_id=$2 AND installation_id=$3 AND repository_id=$4 AND base=$5 AND target=$6 AND observed_at>=now()-interval '1 hour' ORDER BY observed_at DESC LIMIT 20`, w, p, a.installation, a.repo, a.base, a.target)
	if err != nil {
		return err
	}
	var groups [][2]string
	for rows.Next() {
		var g [2]string
		if err = rows.Scan(&g[0], &g[1]); err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, g)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	for _, g := range groups {
		entryRef := "gh-readonly-queue/" + a.target + "/pr-" + strconv.FormatInt(a.n, 10)
		if g[1] != entryRef && !strings.HasPrefix(g[1], entryRef+"-") {
			continue
		}
		ok, err := repo.GroupMatches(ctx, g[0], g[1], a.base, a.head)
		if err != nil {
			var api *github.APIError
			if errors.As(err, &api) && api.Status == 404 {
				continue
			}
			return err
		}
		if !ok {
			continue
		}
		// Scope every success to one signed synthetic commit with exactly
		// the approved base and PR as parents. A multi-PR group waits.
		fresh, err := s.App.InspectPull(ctx, a.installation, a.repo, a.name, a.n, a.issue)
		if err != nil {
			return err
		}
		if !a.subjectValid(fresh) || fresh.Queue.EntryState == "" {
			return a.record(ctx, tx, "held", "The queued pull request changed before its group check", a.group)
		}
		if err = repo.QueueCheck(ctx, g[0], identity(g[0]), subject.Label); err != nil {
			return err
		}
		return a.record(ctx, tx, "passed", "App authorization reported on the verified merge-group commit", g[0])
	}
	return nil
}

func (s Store) submitQueue(ctx context.Context, a permit, key string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ready bool
	err = tx.QueryRowContext(ctx, `SELECT state='submitting' AND expires_at>clock_timestamp() FROM pull_queue_permits
	WHERE (workspace_id,project_id,number,version)=($1,$2,$3,$4) FOR UPDATE`, a.w, a.p, a.n, a.v).Scan(&ready)
	if err != nil || !ready {
		return err
	}
	if err = a.checkContext(ctx, tx); err != nil {
		return err
	}
	o, err := s.App.InspectPull(ctx, a.installation, a.repo, a.name, a.n, a.issue)
	if err != nil {
		return err
	}
	if !a.subjectValid(o) {
		return a.record(ctx, tx, "held", "Remote approvals, target or candidate changed", "")
	}
	repo, err := s.App.QueueRepository(ctx, a.installation, a.repo, a.name, true)
	if err != nil {
		return err
	}
	defer repo.Close()
	subject, err := repo.QueueSubject(ctx, a.n, a.target)
	if errors.Is(err, github.ErrSubjectChanged) {
		return a.record(ctx, tx, "held", "GitHub pull request changed repository or target", a.group)
	}
	if err != nil {
		return err
	}
	if !subject.Guarded || subject.Head != a.head || subject.Base != a.base {
		return a.record(ctx, tx, "held", "GitHub protections or branch changed", "")
	}
	if err = repo.QueueCheck(ctx, a.head, key, subject.Label); err != nil {
		return err
	}
	if !subject.CanEnqueue {
		return a.record(ctx, tx, "awaiting_queue", "Head check passed. Add this pull request to the configured GitHub queue.", "")
	}
	if err = repo.Enqueue(ctx, a.n, a.head, a.base, a.target); err != nil {
		return err
	}
	return a.record(ctx, tx, "queued", "GitHub confirmed queue admission", "")
}
