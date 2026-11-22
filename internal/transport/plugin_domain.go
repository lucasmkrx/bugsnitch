// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
)

type reportArgs struct {
	Project   string `json:"projectId"`
	Observed  string `json:"observed"`
	Expected  string `json:"expected"`
	Steps     string `json:"steps"`
	RequestID string `json:"requestId"`
}
type answerArgs struct {
	DecisionID string `json:"decisionId"`
	Answer     string `json:"answer"`
	Version    int64  `json:"version"`
	RequestID  string `json:"requestId"`
}

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

// The HTTP edge has authenticated the grant and checked the tool-specific scope.
// The worker independently resolves every subject in the single owner's workspace.
func (s *Server) integrationDomain(ctx context.Context, op string, actor [2]string, input json.RawMessage) (any, error) {
	if actor[0] != "plugin" || (actor[1] != "oauth-owner-grant" && actor[1] != "local-grant") {
		return nil, denied
	}

	const ws = "personal"
	call := domainCall{Body: input}
	switch op {
	case "plugin.list_projects", "plugin.list_attempts", "plugin.list_decisions":
		if err := call.decode(&struct{}{}); err != nil {
			return nil, err
		}
		view := map[string]string{"plugin.list_projects": "projects", "plugin.list_attempts": "attempts", "plugin.list_decisions": "decisions"}[op]
		var rows json.RawMessage
		err := s.DB.QueryRowContext(ctx, "SELECT COALESCE(json_agg(t),'[]') FROM (SELECT * FROM ("+views[view]+") v LIMIT 100)t", ws).Scan(&rows)
		return rows, err
	case "plugin.report_incident":
		var a reportArgs
		if err := call.decode(&a); err != nil {
			return nil, err
		}
		id, err := s.Incidents.Report(ctx, ws, a.Project, a.Observed, a.Expected, a.Steps, a.RequestID)
		if err != nil {
			return nil, bad("Report rejected. Check project, required fields and request ID.")
		}
		return object{"id": id}, nil
	case "plugin.answer_decision":
		var a answerArgs
		if err := call.decode(&a); err != nil {
			return nil, err
		}
		err := s.applyAnswer(ctx, ws, a.DecisionID, decisionAnswer{Answer: a.Answer, Version: a.Version, RequestID: a.RequestID})
		return object{"id": a.DecisionID, "answer": a.Answer}, err
	case "plugin.submit_feedback":
		var a messageInput
		if err := call.decode(&a); err != nil {
			return nil, err
		}
		return s.saveMessage(ctx, ws, a)
	case "plugin.request_attempt_control":
		var a controlArgs
		if err := call.decode(&a); err != nil {
			return nil, err
		}
		return s.requestControl(ctx, ws, a.AttemptID, a.controlInput)
	case "plugin.propose_ticket_plan":
		var a proposalArgs
		if err := call.decode(&a); err != nil {
			return nil, err
		}
		if !keyPattern.MatchString(a.RequestID) {
			return nil, bad("Use a stable request ID")
		}
		if _, err := a.Plan.Order(); err != nil {
			return nil, bad("Invalid ticket plan")
		}
		id, err := (planning.Store{DB: s.DB}).Propose(ctx, ws, a.ProjectID, a.RequestID, a.Version, a.Plan)
		return object{"id": id, "status": "proposed", "url": s.Origin + "/#/plans?project=" + url.QueryEscape(a.ProjectID)}, err
	case "plugin.get_project_status":
		var a projectSubject
		if err := call.decode(&a); err != nil {
			return nil, err
		}
		var name string
		var paused bool
		var version, running, decisions, incidents int
		err := s.DB.QueryRowContext(ctx, `SELECT p.name,p.paused,p.version,(SELECT count(*) FROM attempt_reports WHERE project_id=p.id AND state='running'),(SELECT count(*) FROM decisions WHERE project_id=p.id AND answer IS NULL AND subject_version=p.version AND expires_at>now()),(SELECT count(*) FROM incidents WHERE project_id=p.id) FROM projects p WHERE workspace_id=$1 AND id=$2`, ws, a.ProjectID).Scan(&name, &paused, &version, &running, &decisions, &incidents)
		return object{"name": name, "paused": paused, "version": version, "lastReportedRunning": running, "pendingDecisions": decisions, "incidents": incidents, "url": s.Origin + "/#/overview?project=" + url.QueryEscape(a.ProjectID)}, err
	case "plugin.inspect_attempt":
		var a attemptSubject
		if err := call.decode(&a); err != nil {
			return nil, err
		}
		var raw json.RawMessage
		err := s.DB.QueryRowContext(ctx, `SELECT jsonb_build_object('phase',a.phase,'state',a.state,'reason',a.reason_code,'baseSha',a.base_sha,'candidateSha',a.candidate_sha,'evidence',a.evidence,'version',a.version,'lastReport',a.updated_at,'lastDeviceContact',d.last_contact,'revoked',d.revoked) FROM attempt_reports a JOIN devices d ON d.id=a.device_id WHERE a.workspace_id=$1 AND a.project_id=$2 AND a.id=$3`, ws, a.ProjectID, a.AttemptID).Scan(&raw)
		return object{"attempt": raw, "url": s.Origin + "/#/work?project=" + url.QueryEscape(a.ProjectID) + "&id=" + url.QueryEscape(a.AttemptID)}, err
	default:
		return nil, bad("Unknown plugin operation")
	}
}
