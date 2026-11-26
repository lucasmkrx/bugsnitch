// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func (s *Server) deviceProjectReadiness(ctx context.Context, raw json.RawMessage) (any, error) {
	var envelope struct {
		Token string
		Body  json.RawMessage
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, bad("Invalid readiness request")
	}
	var req protocol.ProjectReadinessRequest
	if err := (&domainCall{Body: envelope.Body}).decode(&req); err != nil {
		return nil, err
	}
	if req.Protocol != protocol.Version || req.Project == "" {
		return nil, bad("Choose an assigned project")
	}
	var result protocol.HostedProjectReadiness
	var environments []byte
	err := s.DB.QueryRowContext(ctx, `SELECT p.repository,coalesce(g.active,false),
		coalesce((SELECT json_agg(DISTINCT m.environment ORDER BY m.environment) FROM deployment_monitors m
		 WHERE (m.workspace_id,m.project_id,m.installation_id,m.repository_id)=(g.workspace_id,g.project_id,g.installation_id,g.repository_id) AND g.active AND m.active),'[]')
		FROM devices d JOIN device_projects a ON (a.workspace_id,a.device_id)=(d.workspace_id,d.id)
		JOIN projects p ON (p.workspace_id,p.id)=(a.workspace_id,a.project_id)
		LEFT JOIN github_mappings g ON (g.workspace_id,g.project_id)=(p.workspace_id,p.id)
		WHERE d.token_hash=$1 AND NOT d.revoked AND a.project_id=$2`, envelope.Token, req.Project).
		Scan(&result.Repository, &result.GitHubConnected, &environments)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, denied
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(environments, &result.DeploymentEnvironments); err != nil {
		return nil, err
	}
	return result, nil
}
