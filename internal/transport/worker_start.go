// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

const workerStartFields = `id,device_id,project_id,issue_number,coalesce(plan_id,''),policy_version,status,
 coalesce(attempt_id,''),reason,title,github_id,digest,revision,expires_at`

func scanWorkerStart(row interface{ Scan(...any) error }) (v protocol.WorkerStart, err error) {
	err = row.Scan(&v.ID, &v.Device, &v.Project, &v.Issue, &v.Plan, &v.Policy, &v.Status, &v.Attempt, &v.Reason, &v.Title,
		&v.Revision.GitHubID, &v.Revision.Digest, &v.Revision.Revision, &v.Expires)
	return
}

// Admission is the durable receipt. A disconnected runner need not acknowledge a claimed start twice.
func refreshWorkerStarts(ctx context.Context, tx *sql.Tx, ws, project string) error {
	_, err := tx.ExecContext(ctx, `UPDATE worker_starts s SET status='started',attempt_id=a.attempt_id,reason=''
		FROM admission a WHERE s.workspace_id=$1 AND s.project_id=$2 AND s.status='starting' AND a.attempt_id=s.id
		AND (s.workspace_id,s.project_id,s.device_id,s.plan_id,s.issue_number)=(a.workspace_id,a.project_id,a.device_id,a.plan_id,a.issue_number)`, ws, project)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `WITH pending AS (
		SELECT s.id, CASE
		  WHEN s.expires_at<=now() THEN 'Start request expired'
		  WHEN p.paused OR w.stopped THEN 'Project or workspace dispatch was paused'
		  WHEN s.policy_version<>p.policy_version THEN 'The project policy changed'
		  WHEN NOT EXISTS(SELECT FROM device_projects m JOIN devices d ON (m.workspace_id,m.device_id)=(d.workspace_id,d.id)
		    WHERE (m.workspace_id,m.project_id,m.device_id)=(s.workspace_id,s.project_id,s.device_id) AND NOT d.revoked) THEN 'The paired device was revoked or unassigned'
		  WHEN NOT EXISTS(SELECT FROM planned_issue_state z JOIN github_mappings g USING(workspace_id,project_id,installation_id,repository_id)
		    WHERE (z.workspace_id,z.project_id,z.number)=(s.workspace_id,s.project_id,s.issue_number) AND g.active AND z.source_state='open'
		    AND (z.github_id,z.digest,z.selection_revision)=(s.github_id,s.digest,s.revision)) THEN 'The GitHub ticket revision or repository mapping changed'
		  WHEN EXISTS(SELECT FROM plan_versions v JOIN plan_versions newer ON newer.project_id=v.project_id AND newer.version>v.version
		    WHERE v.id=s.plan_id AND (newer.approved_at IS NOT NULL OR v.approved_at IS NULL)) THEN 'A newer project plan replaced the one selected for this ticket'
		END reason
		FROM worker_starts s JOIN projects p ON (p.workspace_id,p.id)=(s.workspace_id,s.project_id) JOIN workspaces w ON w.id=s.workspace_id
		WHERE s.workspace_id=$1 AND s.project_id=$2 AND s.status IN ('queued','planning','awaiting_plan','starting'))
		UPDATE worker_starts s SET status=CASE WHEN s.expires_at<=now() THEN 'expired' ELSE 'failed' END,reason=pending.reason
		FROM pending WHERE pending.id=s.id AND pending.reason IS NOT NULL`, ws, project)
	if err != nil {
		return err
	}
	// A human proposal can be approved while the selected runner is waiting.
	// Adopt it only if it sealed this ticket's current canonical generation.
	_, err = tx.ExecContext(ctx, `WITH current AS (SELECT id FROM plan_versions WHERE workspace_id=$1 AND project_id=$2 AND approved_at IS NOT NULL ORDER BY version DESC LIMIT 1)
		UPDATE worker_starts s SET plan_id=v.id,status='queued',reason='' FROM current v JOIN plan_issue_bindings b ON b.plan_id=v.id
		JOIN planned_issue_state z ON (z.workspace_id,z.project_id,z.number,z.generation)=(b.workspace_id,b.project_id,b.number,b.generation)
		WHERE s.workspace_id=$1 AND s.project_id=$2 AND s.status IN ('queued','planning') AND s.plan_id IS NULL AND s.issue_number=b.number`, ws, project)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `WITH holds AS (SELECT s.id,CASE
		WHEN coalesce((SELECT approved_at IS NULL FROM plan_versions WHERE project_id=s.project_id ORDER BY version DESC LIMIT 1),false)
		 THEN 'Review the existing project plan proposal before planning this ticket'
		WHEN EXISTS(SELECT FROM worker_starts other WHERE other.project_id=s.project_id AND other.status IN ('planning','awaiting_plan') AND other.id<>s.id)
		 THEN 'Waiting for the preceding ticket proposal to be reviewed'
		WHEN EXISTS(SELECT FROM worker_starts other WHERE other.project_id=s.project_id AND other.plan_id IS NOT NULL AND other.status IN ('queued','starting') AND other.id<>s.id)
		 THEN 'Waiting for the earlier approved ticket to enter execution'
		ELSE '' END reason FROM worker_starts s WHERE s.workspace_id=$1 AND s.project_id=$2 AND s.status IN ('queued','planning') AND s.plan_id IS NULL)
		UPDATE worker_starts s SET reason=holds.reason FROM holds WHERE s.id=holds.id AND s.reason<>holds.reason`, ws, project)
	return err
}

func (s *Server) queueWorkerStart(c *domainCall) error {
	var in struct {
		RequestID, DeviceID, PlanID string
		PolicyVersion               int64
	}
	if err := c.decode(&in); err != nil {
		return err
	}
	n, err := strconv.Atoi(c.Number)
	if err != nil || n < 1 || n > 2147483647 || !keyPattern.MatchString(in.RequestID) || in.DeviceID == "" || in.PolicyVersion < 1 {
		return bad("Choose a device, a current policy and a positive issue number")
	}
	var start protocol.WorkerStart
	err = transaction(c.ctx, s.DB, func(tx *sql.Tx) error {
		ws, project := c.Workspace, c.Project
		var policy int64
		var paused bool
		if err := tx.QueryRowContext(c.ctx, `SELECT p.policy_version,p.paused OR w.stopped FROM projects p JOIN workspaces w ON w.id=p.workspace_id
			WHERE p.workspace_id=$1 AND p.id=$2 FOR UPDATE OF p`, ws, project).Scan(&policy, &paused); err != nil {
			return err
		}
		if err := refreshWorkerStarts(c.ctx, tx, ws, project); err != nil {
			return err
		}
		var reqPlan string
		err := tx.QueryRowContext(c.ctx, `SELECT id,requested_plan FROM worker_starts WHERE workspace_id=$1 AND request_id=$2`, ws, in.RequestID).Scan(&start.ID, &reqPlan)
		if err == nil {
			start, err = scanWorkerStart(tx.QueryRowContext(c.ctx, "SELECT "+workerStartFields+" FROM worker_starts WHERE id=$1", start.ID))
			if err == nil && (start.Project != project || start.Device != in.DeviceID || start.Issue != n || start.Policy != in.PolicyVersion || in.PlanID != reqPlan) {
				return conflict("Start request ID was already used for different work")
			}
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if paused || policy != in.PolicyVersion {
			return conflict("Dispatch is paused or the project policy changed")
		}
		var assigned bool
		if err = tx.QueryRowContext(c.ctx, `SELECT EXISTS(SELECT FROM device_projects m JOIN devices d ON (d.workspace_id,d.id)=(m.workspace_id,m.device_id)
			WHERE m.workspace_id=$1 AND m.project_id=$2 AND d.id=$3 AND NOT d.revoked)`, ws, project, in.DeviceID).Scan(&assigned); err != nil {
			return err
		}
		if !assigned {
			return conflict("Choose a currently assigned device")
		}
		var admitted bool
		if err = tx.QueryRowContext(c.ctx, `SELECT EXISTS(SELECT FROM admission WHERE workspace_id=$1 AND project_id=$2 AND issue_number=$3)`, ws, project, n).Scan(&admitted); err != nil {
			return err
		}
		if admitted {
			return conflict("This ticket already has a worker attempt")
		}
		var currentPlan string
		err = tx.QueryRowContext(c.ctx, `SELECT coalesce((SELECT v.id FROM (SELECT * FROM plan_versions WHERE workspace_id=$1 AND project_id=$2 AND approved_at IS NOT NULL ORDER BY version DESC LIMIT 1) v
			WHERE EXISTS(SELECT FROM jsonb_to_recordset(v.document->'tickets') t(number bigint) WHERE t.number=$3)), '')`, ws, project, n).Scan(&currentPlan)
		if err != nil {
			return err
		}
		if in.PlanID != currentPlan {
			return conflict("Refresh the ticket's current approved plan before starting")
		}
		if currentPlan != "" {
			if err = checkPlannedIssue(c.ctx, tx, ws, project, currentPlan, n, false); err != nil {
				return err
			}
		}
		start, err = scanWorkerStart(tx.QueryRowContext(c.ctx, `INSERT INTO worker_starts(id,workspace_id,project_id,device_id,issue_number,request_id,requested_plan,policy_version,
			github_id,digest,revision,title,plan_id,status) SELECT $1,$2,$3,$4,$5::int,$6,$7,$8,z.github_id,z.digest,z.selection_revision,z.title,nullif($7,''),'queued'
			FROM planned_issue_state z JOIN github_mappings g USING(workspace_id,project_id,installation_id,repository_id)
			WHERE z.workspace_id=$2 AND z.project_id=$3 AND z.number=$5::int AND g.active AND z.availability='current' AND z.checked_at>now()-interval '20 minutes'
			AND z.source_state='open' AND z.selection_revision<>'' AND length(z.digest)=64 FOR SHARE OF z,g RETURNING `+workerStartFields,
			random(16), ws, project, in.DeviceID, n, in.RequestID, in.PlanID, policy))
		if errors.Is(err, sql.ErrNoRows) {
			return conflict("Refresh this open GitHub ticket before starting the worker")
		}
		if err != nil {
			return err
		}
		return event(c.ctx, tx, ws, project, "worker.start_requested", start.ID)
	})
	if err != nil {
		return err
	}
	return c.reply(202, start)
}

func (s *Server) listWorkerStarts(c *domainCall) error {
	starts := []protocol.WorkerStart{}
	err := transaction(c.ctx, s.DB, func(tx *sql.Tx) error {
		if err := refreshWorkerStarts(c.ctx, tx, c.Workspace, c.Project); err != nil {
			return err
		}
		rows, err := tx.QueryContext(c.ctx, "SELECT "+workerStartFields+" FROM worker_starts WHERE workspace_id=$1 AND project_id=$2 ORDER BY created_at DESC LIMIT 200", c.Workspace, c.Project)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanWorkerStart(rows)
			if err != nil {
				return err
			}
			starts = append(starts, v)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	boardSync, err := listBoardSync(c.ctx, s.DB, c.Workspace, c.Project)
	if err != nil {
		return err
	}
	return c.reply(200, object{"starts": starts, "boardSync": boardSync})
}

func (s *Server) deviceWorkerStart(ctx context.Context, raw json.RawMessage) (any, error) {
	var envelope struct {
		Token string
		Body  json.RawMessage
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, bad("Invalid worker request")
	}
	var req protocol.WorkerStartPoll
	if err := (&domainCall{Body: envelope.Body}).decode(&req); err != nil {
		return nil, err
	}
	if req.Protocol != protocol.Version || req.Project == "" {
		return nil, bad("Invalid worker scope")
	}
	var start *protocol.WorkerStart
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var ws, device string
		err := tx.QueryRowContext(ctx, `SELECT d.workspace_id,d.id FROM devices d JOIN device_projects m ON (d.workspace_id,d.id)=(m.workspace_id,m.device_id)
			JOIN projects p ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id)
			WHERE d.token_hash=$1 AND NOT d.revoked AND p.id=$2 FOR UPDATE OF p FOR SHARE OF d,m`, envelope.Token, req.Project).Scan(&ws, &device)
		if errors.Is(err, sql.ErrNoRows) {
			return denied
		}
		if err != nil {
			return err
		}
		ctx = audit.WithActor(ctx, audit.Device, device)
		if err = refreshWorkerStarts(ctx, tx, ws, req.Project); err != nil {
			return err
		}
		switch req.Operation {
		case "poll":
			v, err := scanWorkerStart(tx.QueryRowContext(ctx, `SELECT `+workerStartFields+` FROM worker_starts s
				WHERE workspace_id=$1 AND project_id=$2 AND device_id=$3 AND status IN ('queued','planning','awaiting_plan','starting')
				AND reason=''
				AND (status<>'awaiting_plan' OR EXISTS(SELECT FROM plan_versions v WHERE v.id=s.plan_id AND v.approved_at IS NOT NULL))
				ORDER BY created_at LIMIT 1 FOR UPDATE`, ws, req.Project, device))
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if v.Plan == "" {
				v.Status = "planning"
			} else {
				v.Status = "starting"
			}
			if _, err = tx.ExecContext(ctx, "UPDATE worker_starts SET status=$2 WHERE id=$1", v.ID, v.Status); err != nil {
				return err
			}
			start = &v
		case "proposal", "fail":
			v, err := scanWorkerStart(tx.QueryRowContext(ctx, "SELECT "+workerStartFields+" FROM worker_starts WHERE id=$1 AND device_id=$2 AND project_id=$3 AND workspace_id=$4 FOR UPDATE", req.ID, device, req.Project, ws))
			if err != nil {
				return err
			}
			start = &v
			if req.Operation == "fail" {
				if v.Status != "planning" && v.Status != "starting" {
					return nil
				}
				if req.Reason != "planning_failed" && req.Reason != "dispatch_failed" && req.Reason != "proposal_invalid" {
					return bad("Unknown worker failure")
				}
				reason := "The paired runner could not prepare this ticket. Inspect the local runner log."
				if req.Reason == "proposal_invalid" {
					reason = "The proposed ticket could not be added within the current project plan limits. Review the project scope before retrying."
				}
				_, err = tx.ExecContext(ctx, "UPDATE worker_starts SET status='failed',reason=$2 WHERE id=$1", v.ID, reason)
				if err == nil {
					v.Status, v.Reason = "failed", reason
				}
				return err
			}
			if v.Plan != "" {
				return nil
			} // idempotent after a lost upload receipt
			if v.Status != "planning" || req.Proposal == nil {
				return conflict("This start is no longer awaiting a proposal")
			}
			if v.Reason != "" {
				return conflict(v.Reason)
			}
			pid, err := workerProposal(ctx, tx, ws, v, *req.Proposal)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE worker_starts SET status='awaiting_plan',plan_id=$2 WHERE id=$1", v.ID, pid); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE projects SET version=version+1 WHERE id=$1", req.Project); err != nil {
				return err
			}
			v.Status, v.Plan = "awaiting_plan", pid
			return event(ctx, tx, ws, req.Project, "plan.proposed", pid)
		default:
			return bad("Unknown worker operation")
		}
		return nil
	})
	return object{"start": start}, err
}

// The project row is locked. An intake proposal extends the latest reviewed
// scope and keeps previous ticket generations sealed to that approval.
func workerProposal(ctx context.Context, tx *sql.Tx, ws string, v protocol.WorkerStart, addition domain.Plan) (string, error) {
	if len(addition.Tickets) != 1 || addition.Tickets[0].Number != v.Issue {
		return "", bad("The proposal must cover exactly the selected GitHub ticket")
	}
	if _, err := addition.Order(); err != nil {
		return "", bad(err.Error())
	}
	doc := addition
	var oldID string
	var original []byte
	var version int
	var approved bool
	err := tx.QueryRowContext(ctx, `SELECT id,document,version,approved_at IS NOT NULL FROM plan_versions WHERE workspace_id=$1 AND project_id=$2 ORDER BY version DESC LIMIT 1`, ws, v.Project).Scan(&oldID, &original, &version, &approved)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", err
	case !approved:
		return "", conflict("Review the current project proposal before planning another ticket")
	default:
		doc = domain.Plan{}
		if err = json.Unmarshal(original, &doc); err != nil {
			return "", err
		}
		doc.Tickets = append(doc.Tickets, addition.Tickets[0])
		if addition.Assumptions != "" {
			doc.Assumptions = strings.TrimSpace(doc.Assumptions + fmt.Sprintf("\n#%d — %s", v.Issue, addition.Assumptions))
		}
	}
	order, err := doc.Order()
	if err != nil {
		return "", bad("The combined project scope needs owner review: " + err.Error())
	}
	pid := random(16)
	data, _ := json.Marshal(doc)
	sorted, _ := json.Marshal(order)
	if _, err = tx.ExecContext(ctx, `INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order) VALUES($1,$2,$3,$4,$5,$6)`, pid, ws, v.Project, version+1, data, sorted); err != nil {
		return "", err
	}
	if oldID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO plan_issue_bindings(workspace_id,project_id,plan_id,number,generation)
			SELECT workspace_id,project_id,$1,number,generation FROM plan_issue_bindings WHERE plan_id=$2`, pid, oldID); err != nil {
			return "", err
		}
	}
	if err = planning.BindIssues(ctx, tx, ws, v.Project, pid, addition); err != nil {
		return "", err
	}
	return pid, checkPlannedIssue(ctx, tx, ws, v.Project, pid, v.Issue, false)
}

// Preserve previously attested prerequisites only for unchanged canonical tickets
// when the owner approves a generated extension. The original evidence remains
// available under its original plan and timestamp.
func carryPortalPrerequisites(ctx context.Context, tx *sql.Tx, workspace, project, plan string) error {
	_, err := tx.ExecContext(ctx, `WITH extension AS (
		SELECT v.* FROM worker_starts s JOIN plan_versions v ON v.id=s.plan_id
		WHERE (s.workspace_id,s.project_id,s.plan_id)=($1,$2,$3) AND s.requested_plan='' AND s.status='awaiting_plan'
	), previous AS (
		SELECT v.* FROM plan_versions v,extension x WHERE v.project_id=x.project_id AND v.version<x.version AND v.approved_at IS NOT NULL ORDER BY v.version DESC LIMIT 1
	)
	INSERT INTO prerequisite_evidence(workspace_id,project_id,plan_id,issue_number,revision,summary)
	SELECT $1,$2,x.id,e.issue_number,e.revision,e.summary FROM extension x,previous p
	JOIN prerequisite_evidence e ON e.plan_id=p.id
	JOIN plan_issue_bindings old ON old.plan_id=p.id AND old.number=e.issue_number
	WHERE EXISTS(SELECT FROM plan_issue_bindings cur WHERE cur.plan_id=x.id AND (cur.number,cur.generation)=(old.number,old.generation))
	AND EXISTS(SELECT FROM jsonb_array_elements(p.document->'tickets') t WHERE (t->>'number')::bigint=e.issue_number AND x.document->'tickets' @> jsonb_build_array(t))
	ON CONFLICT DO NOTHING`, workspace, project, plan)
	return err
}
