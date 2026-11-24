// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

// A named application command on the private local socket. Only the Rust
// listener supplies the authenticated workspace and principal. No URL,
// cookie, bearer token, SQL or HTTP headers are accepted by the worker.
type domainCall struct {
	Workspace   string          `json:"workspace"`
	ID          string          `json:"id,omitempty"`
	Project     string          `json:"project,omitempty"`
	Number      string          `json:"number,omitempty"`
	Environment string          `json:"environment,omitempty"`
	Body        json.RawMessage `json:"body,omitempty"`
	ctx         context.Context
	result      domainValue
}
type domainValue struct {
	Status int `json:"status"`
	Body   any `json:"body"`
}

func (c *domainCall) decode(v any) error {
	d := json.NewDecoder(bytes.NewReader(c.Body))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return bad("Invalid JSON request")
	}
	return nil
}
func (c *domainCall) reply(status int, body any) error {
	c.result = domainValue{status, body}
	return nil
}

type DomainRequest struct {
	Version   int             `json:"version"`
	Operation string          `json:"operation"`
	Actor     [2]string       `json:"actor"`
	Input     json.RawMessage `json:"input"`
}
type DomainResponse struct {
	Version int    `json:"version"`
	Status  int    `json:"status"`
	Value   any    `json:"value,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) Domain(ctx context.Context, r DomainRequest) DomainResponse {
	value, err := s.callDomain(ctx, r)
	if err == nil {
		return DomainResponse{Version: 1, Status: 200, Value: value}
	}
	var p problem
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &p):
	case errors.Is(err, sql.ErrNoRows):
		p = problem{404, "Record unavailable"}
	case errors.As(err, &pg) && pg.Code == "23505":
		p = problem{409, "Record already exists"}
	default:
		p = problem{500, "Request failed"}
	}
	return DomainResponse{Version: 1, Status: p.code, Error: p.message}
}
func (s *Server) callDomain(ctx context.Context, r DomainRequest) (any, error) {
	if r.Version != 1 {
		return nil, bad("Unsupported domain protocol")
	}
	if r.Operation == "system.ready" {
		return true, s.DB.PingContext(ctx)
	}
	if r.Actor[0] == "plugin" && (r.Actor[1] == "local-grant" || r.Actor[1] == "oauth-owner-grant") {
		return s.integrationDomain(audit.WithActor(ctx, audit.Plugin, r.Actor[1]), r.Operation, r.Actor, r.Input)
	}
	if r.Actor[1] == "" {
		return nil, denied
	}
	role := audit.Source(r.Actor[0])
	call := domainCall{ctx: audit.WithActor(ctx, role, r.Actor[1])}
	if json.Unmarshal(r.Input, &call) != nil {
		return nil, bad("Invalid command")
	}
	if err := s.authorizeActor(&call, r.Actor[1], role, r.Operation); err != nil {
		return nil, err
	}
	op := map[string]func(*domainCall) error{
		"project.create":         s.createProject,
		"project.connect_github": s.connectGitHub,
		"project.policy":         s.policy, "project.retention": s.retention, "workspace.limits": s.limits,
		"device.limits":    s.deviceLimits,
		"project.dispatch": s.dispatch, "decision.answer": s.answer, "message.create": s.message,
		"plan.propose": s.proposePlan, "plan.approve": s.approvePlan, "plan.evidence": s.attestPrerequisite,
		"publication.propose": s.publication, "publication.approve": s.publish,
		"pull.queue": s.queuePull, "pull.refresh": s.refreshPull, "pull.evidence": s.recordPull, "pull.draft": s.requestDraft,
		"incident.report": s.report, "incident.promote": s.promote,
		"incident.verdict": s.incidentVerdict, "incident.receipt": s.incidentReceipt,
		"linear.link":   s.linkLinear,
		"pull.handover": s.recordHandover,
		"pull.watch":    s.watchPull,
		"pull.approve":  s.approvePull,
	}[r.Operation]
	if op == nil {
		return nil, bad("Unknown domain operation")
	}
	if err := op(&call); err != nil {
		return nil, err
	}
	return call.result, nil
}

func (s *Server) authorizeActor(call *domainCall, id string, role audit.Source, op string) error {
	ctx, workspace := call.ctx, call.Workspace
	query := "SELECT EXISTS(SELECT FROM workspaces WHERE id=$1 AND account_id=$2)"
	args := []any{workspace, id}
	if role != audit.Owner {
		allowed := op == "incident.verdict" || op == "pull.refresh" || op == "incident.report"
		switch role {
		case audit.Operator:
		case audit.Approver:
			allowed = allowed || op == "decision.answer" || op == "plan.approve" ||
				op == "pull.evidence" || op == "pull.handover" || op == "pull.approve"
		default:
			return denied
		}
		if !allowed {
			return denied
		}
		// Object-only routes resolve their project from durable state. A caller
		// cannot grant itself access by supplying a second project ID.
		subject := call.Project
		scope := "$4"
		switch op {
		case "incident.report":
			var b struct{ ProjectID string }
			if json.Unmarshal(call.Body, &b) != nil {
				return bad("Invalid incident report")
			}
			subject = b.ProjectID
		case "plan.approve":
			scope = "(SELECT project_id FROM plan_versions WHERE workspace_id=$1 AND id=$4)"
			subject = call.ID
		case "decision.answer":
			scope = "(SELECT project_id FROM decisions WHERE workspace_id=$1 AND id=$4)"
			subject = call.ID
		}
		query = "SELECT EXISTS(SELECT FROM workspace_members WHERE workspace_id=$1 AND id=$2 AND role=$3 AND revoked_at IS NULL AND (project_id IS NULL OR project_id=" + scope + "))"
		args = append(args, role, subject)
	}
	var authorized bool
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&authorized); err != nil {
		return err
	}
	if !authorized {
		return denied
	}
	return nil
}
func (s *Server) incidentVerdict(c *domainCall) error {
	v, err := s.Incidents.Verdict(c.ctx, c.Workspace, c.Project, c.ID, c.Environment)
	if err != nil {
		return incidentError(err)
	}
	return c.reply(200, v)
}
func (s *Server) incidentReceipt(c *domainCall) error {
	var receipt incidents.Receipt
	if err := c.decode(&receipt); err != nil {
		return err
	}
	if err := s.Incidents.RecordReceipt(c.ctx, c.Workspace, c.Project, c.ID, receipt); err != nil {
		return incidentError(err)
	}
	return c.reply(200, object{"commandId": receipt.CommandID, "status": "recorded"})
}
