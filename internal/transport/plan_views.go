// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
)

type planView struct {
	ID             string                  `json:"id"`
	Project        string                  `json:"projectId"`
	Version        int                     `json:"version"`
	Title          string                  `json:"title"`
	Document       domain.Plan             `json:"document"`
	ExecutionOrder json.RawMessage         `json:"executionOrder"`
	CreatedAt      time.Time               `json:"createdAt"`
	Status         string                  `json:"status"`
	Forecast       domain.ProgressForecast `json:"progressForecast"`
}

// Read the plan, its evidence, and its current GitHub bindings in one snapshot.
// A worker's candidate and the disappearance of an error cannot prove delivery.
func (s *Server) listPlans(ctx context.Context, ws string) ([]planView, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT v.id,v.project_id,v.version,v.document,v.execution_order,v.created_at,v.approved_at,
    NOT EXISTS(SELECT FROM plan_versions newer WHERE newer.workspace_id=v.workspace_id AND newer.project_id=v.project_id AND newer.approved_at IS NOT NULL AND newer.version>v.version),
    w.stopped OR p.paused, now(),
    COALESCE((SELECT jsonb_agg(jsonb_build_object('number',t.number,'recordedAt',e.created_at,'current',
        z.number IS NULL OR coalesce(m.active AND z.availability='current' AND z.source_state IN ('open','closed') AND z.checked_at>now()-interval '20 minutes' AND b.generation=z.generation,false)))
      FROM jsonb_to_recordset(v.document->'tickets') AS t(number bigint)
      LEFT JOIN prerequisite_evidence e ON (e.workspace_id,e.project_id,e.plan_id,e.issue_number)=(v.workspace_id,v.project_id,v.id,t.number)
      LEFT JOIN planned_issue_state z ON (z.workspace_id,z.project_id,z.number)=(v.workspace_id,v.project_id,t.number)
      LEFT JOIN github_mappings m ON (m.workspace_id,m.project_id,m.installation_id,m.repository_id)=(z.workspace_id,z.project_id,z.installation_id,z.repository_id)
      LEFT JOIN plan_issue_bindings b ON (b.workspace_id,b.project_id,b.plan_id,b.number)=(v.workspace_id,v.project_id,v.id,t.number)), '[]')
    FROM plan_versions v JOIN projects p ON (p.workspace_id,p.id)=(v.workspace_id,v.project_id) JOIN workspaces w ON w.id=v.workspace_id
    WHERE v.workspace_id=$1 ORDER BY v.created_at DESC,v.id LIMIT 150`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := []planView{}
	for rows.Next() {
		var v planView
		var raw, evidence []byte
		var approved *time.Time
		var current, paused bool
		var now time.Time
		if err = rows.Scan(&v.ID, &v.Project, &v.Version, &raw, &v.ExecutionOrder, &v.CreatedAt, &approved, &current, &paused, &now, &evidence); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v.Document); err != nil {
			return nil, err
		}
		var observed []domain.CompletionEvidence
		if err = json.Unmarshal(evidence, &observed); err != nil {
			return nil, err
		}
		v.Title = v.Document.Title
		v.Status = "proposed"
		if approved != nil {
			v.Status = "approved"
		}
		v.Forecast = domain.ForecastProgress(v.Document, approved, current, paused, now, observed)
		plans = append(plans, v)
	}
	return plans, rows.Err()
}
