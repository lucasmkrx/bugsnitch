// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"encoding/json"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"net/http"
	"slices"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
)

// Admission is requested by a human-initiated local execution. No command starts a model remotely.
func (s *Server) admission(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var a protocol.Admission
	if err := decode(w, r, &a); err != nil {
		return err
	}
	if err := compatible(a.Protocol); err != nil {
		return err
	}
	if a.Sequence < 1 || !keyPattern.MatchString(a.AttemptID) || !shaPattern.MatchString(a.BaseSHA) || a.IssueNumber < 1 || !slices.Contains([]string{"admit", "finish", "turn_start", "turn_end"}, a.Operation) {
		return bad("Invalid admission request")
	}
	if a.Operation == "finish" && !slices.Contains([]string{"waiting", "completed", "failed", "cancelled"}, a.State) {
		return bad("Provide the demonstrated local outcome")
	}
	ctx := r.Context()
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var device, ws string
		var deviceLimit int
		err := tx.QueryRowContext(ctx, `SELECT d.id,d.workspace_id,d.turn_limit FROM devices d JOIN device_projects m ON m.device_id=d.id WHERE d.token_hash=$1 AND NOT d.revoked AND m.project_id=$2 FOR UPDATE OF d`, hash(token), a.ProjectID).Scan(&device, &ws, &deviceLimit)
		if err == sql.ErrNoRows {
			return denied
		}
		if err != nil {
			return err
		}
		// Serialize capacity, ownership and turn changes across *all* devices in a workspace.
		var tickets, turnLimit int
		var stopped bool
		if err = tx.QueryRowContext(ctx, "SELECT ticket_limit,turn_limit,stopped FROM workspaces WHERE id=$1 FOR UPDATE", ws).Scan(&tickets, &turnLimit, &stopped); err != nil {
			return err
		}
		var planID string
		var resources []string
		var paused, requirePlan bool
		var policy int64
		if err = tx.QueryRowContext(ctx, "SELECT paused,require_plan,policy_version FROM projects WHERE workspace_id=$1 AND id=$2 FOR SHARE", ws, a.ProjectID).Scan(&paused, &requirePlan, &policy); err != nil {
			return err
		}
		if (stopped || paused) && (a.Operation == "admit" || a.Operation == "turn_start") {
			return conflict("Dispatch is stopped for this workspace or project")
		}
		var seq int64
		var prev, owner, p, base string
		var issue int
		var active, inTurn bool
		err = tx.QueryRowContext(ctx, "SELECT sequence,operation,device_id,project_id,base_sha,issue_number,active,in_turn,coalesce(plan_id,'') FROM admission WHERE workspace_id=$1 AND attempt_id=$2", ws, a.AttemptID).Scan(&seq, &prev, &owner, &p, &base, &issue, &active, &inTurn, &planID)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if owner != device || p != a.ProjectID || base != a.BaseSHA || issue != a.IssueNumber {
				return denied
			}
			if seq == a.Sequence && prev == a.Operation {
				return nil
			}
		} else if a.Operation != "admit" {
			return conflict("Attempt has no active admission")
		}
		if a.Sequence != seq+1 {
			return conflict("Stale operation sequence; reconcile the original machine")
		}
		switch a.Operation {
		case "admit":
			planID, resources, err = planEligibility(ctx, tx, ws, a)
			if err != nil {
				return err
			}
			if requirePlan && planID == "" {
				return conflict("Approve project scope before starting this issue")
			}
			if !active {
				var used int
				var duplicate bool
				if err = tx.QueryRowContext(ctx, "SELECT count(*) FILTER(WHERE active),COALESCE(bool_or(owns_issue AND attempt_id<>$4 AND project_id=$2 AND issue_number=$3),false) FROM admission WHERE workspace_id=$1", ws, a.ProjectID, a.IssueNumber, a.AttemptID).Scan(&used, &duplicate); err != nil {
					return err
				}
				if used >= tickets || duplicate {
					return conflict("Ticket capacity or issue ownership is already reserved")
				}
			}
			active = true
		case "turn_start":
			if err = checkPlannedIssue(ctx, tx, ws, a.ProjectID, planID, a.IssueNumber, false); err != nil {
				return err
			}
			if !active || inTurn {
				return conflict("Model turn unavailable for this attempt")
			}
			available, e := turns.Available(ctx, tx, ws, device, turnLimit, deviceLimit)
			if e != nil {
				return e
			}
			if !available {
				return conflict("Model-turn capacity is reserved")
			}
			inTurn = true
		case "turn_end":
			if !inTurn {
				return conflict("No matching active turn")
			}
			inTurn = false
		case "finish":
			if inTurn {
				return conflict("Confirm the turn ended before releasing execution capacity")
			}
			active = false
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO admission(workspace_id,project_id,device_id,attempt_id,issue_number,base_sha,sequence,operation,active,in_turn,owns_issue) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(workspace_id,attempt_id) DO UPDATE SET sequence=EXCLUDED.sequence,operation=EXCLUDED.operation,owns_issue=EXCLUDED.owns_issue,active=EXCLUDED.active,in_turn=EXCLUDED.in_turn,updated_at=now()`, ws, a.ProjectID, device, a.AttemptID, a.IssueNumber, a.BaseSHA, a.Sequence, a.Operation, active, inTurn, a.Operation != "finish" || a.State == "waiting")
		if err != nil {
			return err
		}
		if a.Operation == "admit" {
			if resources == nil {
				resources = []string{}
			}
			locks, _ := json.Marshal(resources)
			if _, err = tx.ExecContext(ctx, "UPDATE admission SET plan_id=NULLIF($1,''),resources=$2 WHERE workspace_id=$3 AND attempt_id=$4", planID, locks, ws, a.AttemptID); err != nil {
				return err
			}
		}
		principal := audit.WithActor(ctx, audit.Device, device)
		recorded := audit.WithDetails(principal, audit.Details{Reason: "Original device reconciled its execution admission", SubjectVersion: a.Sequence, PolicyVersion: policy, EvidenceRef: a.BaseSHA})
		return event(recorded, tx, ws, a.ProjectID, "admission."+a.Operation, a.AttemptID)
	})
	if err != nil {
		return err
	}
	return send(w, 200, object{"sequence": a.Sequence})
}
