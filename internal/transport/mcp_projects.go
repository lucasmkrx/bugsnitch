// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/url"
	"slices"
)

type projectSubject struct {
	ProjectID string `json:"projectId"`
}
type attemptSubject struct {
	ProjectID string `json:"projectId"`
	AttemptID string `json:"attemptId"`
}
type proposalArgs struct {
	ProjectID string      `json:"projectId"`
	Version   int64       `json:"version"`
	RequestID string      `json:"requestId"`
	Plan      domain.Plan `json:"plan"`
}
type controlArgs struct {
	AttemptID string `json:"attemptId"`
	controlInput
}

func mcpError(err error) error {
	if err == nil {
		return nil
	}
	var p problem
	if errors.As(err, &p) {
		return p
	}
	return errors.New("Operation unavailable or subject changed. Refresh its current state.")
}
func (s *Server) addProjectTools(server *mcp.Server, ws string, scopes []string) {
	read := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(server, &mcp.Tool{Name: "get_project_status", Description: "Inspect project settings, reported work, decisions and incident counts for a workspace-owned project.", Annotations: read}, func(ctx context.Context, _ *mcp.CallToolRequest, a projectSubject) (*mcp.CallToolResult, any, error) {
		var name string
		var paused bool
		var version, running, decisions, incidents int
		err := s.DB.QueryRowContext(ctx, `SELECT p.name,p.paused,p.version,(SELECT count(*) FROM attempt_reports WHERE project_id=p.id AND state='running'),(SELECT count(*) FROM decisions WHERE project_id=p.id AND answer IS NULL AND subject_version=p.version AND expires_at>now()),(SELECT count(*) FROM incidents WHERE project_id=p.id) FROM projects p WHERE workspace_id=$1 AND id=$2`, ws, a.ProjectID).Scan(&name, &paused, &version, &running, &decisions, &incidents)
		if err != nil {
			return nil, nil, mcpError(err)
		}
		return nil, object{"name": name, "paused": paused, "version": version, "lastReportedRunning": running, "pendingDecisions": decisions, "incidents": incidents, "url": s.Origin + "/#/overview?project=" + url.QueryEscape(a.ProjectID)}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "inspect_attempt", Description: "Inspect revision-bound validation and review counts for one attempt. Source text stays on the owner-selected artifact route.", Annotations: read}, func(ctx context.Context, _ *mcp.CallToolRequest, a attemptSubject) (*mcp.CallToolResult, any, error) {
		var raw json.RawMessage
		err := s.DB.QueryRowContext(ctx, `SELECT jsonb_build_object('phase',a.phase,'state',a.state,'reason',a.reason_code,'baseSha',a.base_sha,'candidateSha',a.candidate_sha,'evidence',a.evidence,'version',a.version,'lastReport',a.updated_at,'lastDeviceContact',d.last_contact,'revoked',d.revoked) FROM attempt_reports a JOIN devices d ON d.id=a.device_id WHERE a.workspace_id=$1 AND a.project_id=$2 AND a.id=$3`, ws, a.ProjectID, a.AttemptID).Scan(&raw)
		if err != nil {
			return nil, nil, mcpError(err)
		}
		return nil, object{"attempt": raw, "url": s.Origin + "/#/work?project=" + url.QueryEscape(a.ProjectID) + "&id=" + url.QueryEscape(a.AttemptID)}, nil
	})
	if slices.Contains(scopes, "feedback.write") {
		mcp.AddTool(server, &mcp.Tool{Name: "submit_feedback", Description: "Record an owner's project message. Hosted model processing remains blocked pending authorization."}, func(ctx context.Context, _ *mcp.CallToolRequest, a messageInput) (*mcp.CallToolResult, any, error) {
			out, err := s.saveMessage(ctx, ws, a)
			return nil, out, mcpError(err)
		})
	}
	if slices.Contains(scopes, "attempt.control") {
		mcp.AddTool(server, &mcp.Tool{Name: "request_attempt_control", Description: "Request pause or stop at the reviewed attempt version. The owning computer confirms quiescence separately."}, func(ctx context.Context, _ *mcp.CallToolRequest, a controlArgs) (*mcp.CallToolResult, any, error) {
			out, err := s.requestControl(ctx, ws, a.AttemptID, a.controlInput)
			return nil, out, mcpError(err)
		})
	}
	if slices.Contains(scopes, "plan.write") {
		mcp.AddTool(server, &mcp.Tool{Name: "propose_ticket_plan", Description: "Stage a versioned plan for new GitHub issues. Numbers are provisional. The owner must approve external publication in Plans."}, func(ctx context.Context, _ *mcp.CallToolRequest, a proposalArgs) (*mcp.CallToolResult, any, error) {
			if !keyPattern.MatchString(a.RequestID) {
				return nil, nil, bad("Use a stable request ID")
			}
			if _, err := a.Plan.Order(); err != nil {
				return nil, nil, err
			}
			id, err := (planning.Store{DB: s.DB}).Propose(ctx, ws, a.ProjectID, a.RequestID, a.Version, a.Plan)
			if err != nil {
				return nil, nil, mcpError(err)
			}
			return nil, object{"id": id, "status": "proposed", "url": s.Origin + "/#/plans?project=" + url.QueryEscape(a.ProjectID)}, nil
		})
	}
}
