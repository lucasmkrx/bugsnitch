// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

var environmentLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

type releaseStage struct {
	Project string `json:"projectId"`
	Number  int64  `json:"pullNumber"`
	Head    string `json:"head"`
	Target  string `json:"target"`
}

func (s *Server) createReleasePlan(call *domainCall) error {
	var b struct {
		Name, Environment, RequestID string
		Stages                       []releaseStage
	}
	if err := call.decode(&b); err != nil {
		return err
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 120 || strings.IndexFunc(b.Name, unicode.IsControl) >= 0 || !keyPattern.MatchString(b.RequestID) || !environmentLabel.MatchString(b.Environment) || len(b.Stages) < 2 || len(b.Stages) > 16 {
		return bad("Provide a release name, environment, request ID, and 2–16 inspected pull requests")
	}
	type pull struct {
		project string
		number  int64
	}
	projects := map[string]bool{}
	seen := map[pull]bool{}
	for _, p := range b.Stages {
		if p.Number < 1 || !shaPattern.MatchString(p.Head) || len(p.Target) > 160 || p.Target == "" || p.Project == "" {
			return bad("Choose inspected PRs with exact heads and targets")
		}
		key := pull{p.Project, p.Number}
		if seen[key] {
			return bad("A pull request can appear only once in a release")
		}
		seen[key] = true
		projects[p.Project] = true
	}
	if len(projects) < 2 {
		return bad("A multi-repository release needs at least two projects")
	}
	ctx, ws := call.ctx, call.Workspace
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		// Serialize plan creation with concurrent owner requests before checking its idempotency key.
		if _, err := tx.ExecContext(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", ws); err != nil {
			return err
		}
		var name, env string
		err := tx.QueryRowContext(ctx, "SELECT name,environment FROM release_plans WHERE workspace_id=$1 AND id=$2", ws, b.RequestID).Scan(&name, &env)
		if err == nil {
			var raw []byte
			if err = tx.QueryRowContext(ctx, `SELECT json_agg(json_build_object('projectId',project_id,'pullNumber',pull_number,'head',head,'target',target) ORDER BY position)
       FROM release_stages WHERE workspace_id=$1 AND release_id=$2`, ws, b.RequestID).Scan(&raw); err != nil {
				return err
			}
			var stored []releaseStage
			if err = json.Unmarshal(raw, &stored); err != nil {
				return err
			}
			if name != b.Name || env != b.Environment || len(stored) != len(b.Stages) {
				return conflict("Release request ID already used")
			}
			for i := range stored {
				if stored[i] != b.Stages[i] {
					return conflict("Release request ID already used")
				}
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO release_plans(workspace_id,id,name,environment) VALUES($1,$2,$3,$4)", ws, b.RequestID, b.Name, b.Environment); err != nil {
			return err
		}
		for i, p := range b.Stages {
			res, e := tx.ExecContext(ctx, `INSERT INTO release_stages(workspace_id,release_id,position,project_id,pull_number,head,target)
     SELECT o.workspace_id,$2,$3,o.project_id,o.number,$6,$7 FROM pull_observations o JOIN github_mappings g USING(workspace_id,project_id)
     WHERE o.workspace_id=$1 AND o.project_id=$4 AND o.number=$5 AND g.active AND o.snapshot->>'head'=$6 AND o.snapshot->>'target'=$7 AND o.snapshot->>'state'='OPEN'
     AND o.observed_at>=now()-interval '10 minutes' AND NOT EXISTS (SELECT FROM release_stage_status s WHERE (s.workspace_id,s.project_id,s.pull_number)=(o.workspace_id,o.project_id,o.number) AND s.cancelled_at IS NULL AND s.status<>'handed_over')
     FOR SHARE OF o,g`, ws, b.RequestID, i+1, p.Project, p.Number, p.Head, p.Target)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n != 1 {
				return conflict("Refresh the open PRs and check their existing release plans")
			}
		}
		return event(ctx, tx, ws, "", "release.planned", b.RequestID)
	})
	if err != nil {
		return err
	}
	return call.reply(201, object{"id": b.RequestID})
}
func (s *Server) cancelReleasePlan(call *domainCall) error {
	var b struct{}
	if err := call.decode(&b); err != nil {
		return err
	}
	err := transaction(call.ctx, s.DB, func(tx *sql.Tx) error {
		var done bool
		if err := tx.QueryRowContext(call.ctx, "SELECT cancelled_at IS NOT NULL FROM release_plans WHERE workspace_id=$1 AND id=$2 FOR UPDATE", call.Workspace, call.ID).Scan(&done); err != nil {
			return err
		}
		if done {
			return nil
		}
		if _, err := tx.ExecContext(call.ctx, "UPDATE release_plans SET cancelled_at=now() WHERE workspace_id=$1 AND id=$2", call.Workspace, call.ID); err != nil {
			return err
		}
		return event(call.ctx, tx, call.Workspace, "", "release.cancelled", call.ID)
	})
	if err != nil {
		return err
	}
	return call.reply(200, object{"cancelled": true})
}
