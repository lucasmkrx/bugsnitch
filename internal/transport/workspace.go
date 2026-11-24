// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

const inferenceReason = "Hosted inference requires confirmation of this deployment’s eligibility for Sign in with ChatGPT. Local account-backed runs can be started on the computer."

func (s *Server) createProject(call *domainCall) error {
	ws := call.Workspace
	var b struct{ Name, Repository string }
	if err := call.decode(&b); err != nil {
		return err
	}
	b.Name = strings.TrimSpace(b.Name)
	b.Repository = strings.ToLower(strings.TrimSpace(b.Repository))
	if b.Name == "" || len(b.Name) > 100 || !repoPattern.MatchString(b.Repository) || strings.HasSuffix(b.Repository, "/.") || strings.HasSuffix(b.Repository, "/..") {
		return bad("Enter a name and a GitHub owner/repository")
	}
	id := random(16)
	ctx := call.ctx
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO projects(id,workspace_id,name,repository,paused) SELECT $1,$2,$3,$4,stopped FROM workspaces WHERE id=$2 FOR SHARE", id, ws, b.Name, b.Repository); err != nil {
			return err
		}
		return event(ctx, tx, ws, id, "project.created", id)
	})
	if err != nil {
		return err
	}
	return call.reply(201, object{"id": id})
}
func (s *Server) dispatch(call *domainCall) error {
	ws := call.Workspace
	var b struct {
		Paused  *bool
		Version int64
	}
	if err := call.decode(&b); err != nil {
		return err
	}
	if b.Paused == nil || b.Version < 1 {
		return bad("Provide paused and version")
	}
	p := call.ID
	ctx := call.ctx
	var decision string
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var paused bool
		var v int64
		err := tx.QueryRowContext(ctx, "SELECT paused,version FROM projects WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, p).Scan(&paused, &v)
		if err != nil {
			return err
		}
		if v != b.Version {
			return conflict("Project changed. Refresh and review the current version.")
		}
		if paused == *b.Paused {
			return nil
		}
		if *b.Paused {
			if _, err := tx.ExecContext(ctx, "UPDATE projects SET paused=true,version=version+1 WHERE workspace_id=$1 AND id=$2", ws, p); err != nil {
				return err
			}
			ctx = audit.WithDetails(ctx, audit.Details{Reason: "Owner paused new project dispatch", SubjectVersion: v + 1})
			return event(ctx, tx, ws, p, "project.dispatch_paused", p)
		}
		err = tx.QueryRowContext(ctx, "INSERT INTO decisions(id,workspace_id,project_id,subject_version,question) VALUES($1,$2,$3,$4,'Allow this project to accept new work again?') ON CONFLICT(project_id,subject_version) DO NOTHING RETURNING id", random(16), ws, p, v).Scan(&decision)
		if err == sql.ErrNoRows {
			var expired bool
			if err = tx.QueryRowContext(ctx, "SELECT id,expires_at<=now() FROM decisions WHERE project_id=$1 AND subject_version=$2", p, v).Scan(&decision, &expired); err != nil {
				return err
			}
			if !expired {
				return nil
			}
			if _, err = tx.ExecContext(ctx, "UPDATE projects SET version=version+1 WHERE id=$1", p); err != nil {
				return err
			}
			decision = random(16)
			_, err = tx.ExecContext(ctx, "INSERT INTO decisions(id,workspace_id,project_id,subject_version,question) VALUES($1,$2,$3,$4,'Allow this project to accept new work again?')", decision, ws, p, v+1)
		}
		if err != nil {
			return err
		}
		return event(ctx, tx, ws, p, "decision.requested", decision)
	})
	if err != nil {
		return err
	}
	if decision != "" {
		return call.reply(202, object{"decisionId": decision})
	}
	return call.reply(200, object{"paused": *b.Paused})
}

type decisionAnswer struct {
	Answer    string `json:"answer"`
	RequestID string `json:"requestId"`
	Version   int64  `json:"version"`
}

func (s *Server) answer(call *domainCall) error {
	ws := call.Workspace
	var b decisionAnswer
	if err := call.decode(&b); err != nil {
		return err
	}
	id := call.ID
	if err := s.applyAnswer(call.ctx, ws, id, b); err != nil {
		return err
	}
	return call.reply(200, object{"id": id, "answer": b.Answer})
}
func (s *Server) applyAnswer(ctx context.Context, ws, id string, b decisionAnswer) error {
	return transaction(ctx, s.DB, func(tx *sql.Tx) error { return applyAnswerTx(ctx, tx, ws, id, b) })
}
func applyAnswerTx(ctx context.Context, tx *sql.Tx, ws, id string, b decisionAnswer) error {
	if (b.Answer != "Resume dispatch" && b.Answer != "Keep paused") || !keyPattern.MatchString(b.RequestID) || b.Version < 1 {
		return bad("Provide a valid choice, version, and request ID")
	}
	var project string
	var subject, version, current int64
	var prev, key sql.NullString
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT d.project_id,d.subject_version,d.version,d.answer,d.request_id,d.expires_at>now(),p.version FROM decisions d JOIN projects p ON p.id=d.project_id WHERE d.workspace_id=$1 AND d.id=$2 FOR UPDATE OF p,d`, ws, id).Scan(&project, &subject, &version, &prev, &key, &active, &current)
	if err != nil {
		return err
	}
	if prev.Valid {
		if key.String == b.RequestID && prev.String == b.Answer && b.Version+1 == version {
			return nil
		}
		return conflict("This decision already has an answer")
	}
	if b.Answer == "Resume dispatch" {
		var stopped bool
		if err = tx.QueryRowContext(ctx, "SELECT stopped FROM workspaces WHERE id=$1", ws).Scan(&stopped); err != nil {
			return err
		}
		if stopped {
			return conflict("Unlock workspace dispatch first")
		}
	}
	if !active || current != subject || version != b.Version {
		return conflict("Decision is stale. Review the latest project and evidence.")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE projects SET paused=$1,version=version+1 WHERE id=$2 AND workspace_id=$3", b.Answer == "Keep paused", project, ws); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE decisions SET answer=$1,request_id=$2,version=version+1 WHERE id=$3", b.Answer, b.RequestID, id); err != nil {
		return err
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: b.Answer, SubjectVersion: subject, EvidenceRef: id})
	return event(ctx, tx, ws, project, "decision.answered", id)
}

type messageInput struct {
	ProjectID string `json:"projectId"`
	RequestID string `json:"requestId"`
	Body      string `json:"body"`
}

func (s *Server) message(call *domainCall) error {
	ws := call.Workspace
	var b messageInput
	if err := call.decode(&b); err != nil {
		return err
	}
	out, err := s.saveMessage(call.ctx, ws, b)
	if err != nil {
		return err
	}
	return call.reply(202, out)
}
func (s *Server) saveMessage(ctx context.Context, ws string, b messageInput) (object, error) {
	if !keyPattern.MatchString(b.RequestID) || strings.TrimSpace(b.Body) == "" || len(b.Body) > 4000 {
		return nil, bad("Message or request ID is invalid")
	}
	body := incidents.Redact(b.Body)
	var id string
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "INSERT INTO manager_messages(id,workspace_id,project_id,request_id,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(workspace_id,request_id) DO NOTHING RETURNING id", random(16), ws, b.ProjectID, b.RequestID, body).Scan(&id)
		if err == sql.ErrNoRows {
			err = tx.QueryRowContext(ctx, "SELECT id FROM manager_messages WHERE workspace_id=$1 AND project_id=$2 AND request_id=$3 AND body=$4", ws, b.ProjectID, b.RequestID, body).Scan(&id)
			if err == sql.ErrNoRows {
				return conflict("Request ID already used")
			}
			return err
		}
		if err != nil {
			return err
		}
		return event(ctx, tx, ws, b.ProjectID, "message.blocked", id)
	})
	if err != nil {
		return nil, err
	}
	return object{"id": id, "status": "blocked", "reason": inferenceReason}, nil
}
func incidentError(err error) error {
	switch {
	case errors.Is(err, incidents.ErrInvalid):
		return bad("Incident fields are invalid")
	case errors.Is(err, incidents.ErrUnmapped):
		return conflict("The project or its active provider mapping is unavailable")
	case errors.Is(err, incidents.ErrConflict):
		return conflict("Evidence or request changed. Refresh the incident.")
	}
	return err
}
func (s *Server) report(call *domainCall) error {
	ws := call.Workspace
	var b struct{ ProjectID, RequestID, Observed, Expected, Steps string }
	if err := call.decode(&b); err != nil {
		return err
	}
	id, err := s.Incidents.Report(call.ctx, ws, b.ProjectID, b.Observed, b.Expected, b.Steps, b.RequestID)
	if err != nil {
		return incidentError(err)
	}
	return call.reply(201, object{"id": id})
}
func (s *Server) promote(call *domainCall) error {
	ws := call.Workspace
	var b struct{ Version int64 }
	if err := call.decode(&b); err != nil {
		return err
	}
	err := s.Incidents.Promote(call.ctx, ws, call.Project, call.ID, b.Version)
	if err != nil {
		return incidentError(err)
	}
	return call.reply(202, object{"status": "accepted"})
}
