// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"encoding/json"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
)

type policy struct {
	Preset           string `json:"preset"`
	RequirePlan      bool   `json:"requirePlan"`
	AllowPublication bool   `json:"allowPublication"`
	AllowPromotion   bool   `json:"allowPromotion"`
	AllowDraftPR     bool   `json:"allowDraftPR"`
	AutoTriage       bool   `json:"autoTriage"`
}

var presets = map[string]policy{
	"Assisted":                 {Preset: "Assisted", RequirePlan: true},
	"Supervised":               {Preset: "Supervised", AllowPublication: true, AllowPromotion: true, AllowDraftPR: true},
	"Autonomous within limits": {Preset: "Autonomous within limits", RequirePlan: true, AllowPublication: true, AllowPromotion: true, AllowDraftPR: true, AutoTriage: true},
}

func (s *Server) policy(call *domainCall) error {
	ws := call.Workspace
	var b struct {
		Version   int64
		Preset    string
		Overrides *policy
	}
	if err := call.decode(&b); err != nil {
		return err
	}
	next, ok := presets[b.Preset]
	if !ok {
		return bad("Unknown autonomy preset")
	}
	if b.Overrides != nil {
		next = *b.Overrides
		next.Preset = "Custom"
	}
	if next.AutoTriage && !next.AllowPromotion {
		return bad("Automatic triage needs permission to promote incidents")
	}
	p := call.ID
	ctx := call.ctx
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var version, policyV int64
		if err := tx.QueryRowContext(ctx, "SELECT version,policy_version FROM projects WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, p).Scan(&version, &policyV); err != nil {
			return err
		}
		if version != b.Version {
			return conflict("Project settings changed; refresh the policy")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE projects SET preset=$1,require_plan=$2,allow_publication=$3,allow_promotion=$4,allow_draft_pr=$5,auto_triage=$6,version=version+1,policy_version=policy_version+1 WHERE workspace_id=$7 AND id=$8", next.Preset, next.RequirePlan, next.AllowPublication, next.AllowPromotion, next.AllowDraftPR, next.AutoTriage, ws, p); err != nil {
			return err
		}
		raw, _ := json.Marshal(next)
		if _, err := tx.ExecContext(ctx, "INSERT INTO policy_versions(workspace_id,project_id,version,actor,policy) VALUES($1,$2,$3,'owner',$4)", ws, p, policyV+1, raw); err != nil {
			return err
		}
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Approved project policy revision", PolicyVersion: policyV + 1, SubjectVersion: version + 1})
		return event(ctx, tx, ws, p, "policy.changed", p)
	})
	if err != nil {
		return err
	}
	return call.reply(200, next)
}
func (s *Server) limits(call *domainCall) error {
	ws := call.Workspace
	var b struct{ Tickets, Turns, Version int }
	if err := call.decode(&b); err != nil {
		return err
	}
	if b.Tickets < 1 || b.Tickets > 32 || b.Turns < 1 || b.Turns > 32 || b.Version < 1 {
		return bad("Use limits from 1 to 32 and the current limits version")
	}
	ctx := call.ctx
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE workspaces SET ticket_limit=$1,turn_limit=$2,limits_version=limits_version+1 WHERE id=$3 AND limits_version=$4", b.Tickets, b.Turns, ws, b.Version)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return conflict("Limits changed; refresh current settings")
		}
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Workspace capacity limits updated", SubjectVersion: int64(b.Version + 1)})
		return event(ctx, tx, ws, "", "limits.changed", ws)
	})
	if err != nil {
		return err
	}
	return call.reply(200, object{"tickets": b.Tickets, "turns": b.Turns, "version": b.Version + 1})
}
