// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"net/http"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

const inferenceReason = "Hosted inference requires confirmation of this deployment’s eligibility for Sign in with ChatGPT. Local account-backed runs can be started on the computer."

var views = map[string]string{
	"publications": `SELECT x.id,x.project_id AS "projectId",x.version,x.status,x.document,x.plan_id AS "planId", x.created_at AS "createdAt",COALESCE((SELECT json_agg(t ORDER BY position) FROM (SELECT position,local_number AS "localNumber",canonical_number AS "number",status,reason FROM planned_issues WHERE publication_id=x.id)t),'[]') AS issues FROM publications x WHERE x.workspace_id=$1 ORDER BY x.created_at DESC LIMIT 100`,
	"projects":     `SELECT id,name,repository,paused,version,preset,policy_version AS "policyVersion",require_plan AS "requirePlan",allow_publication AS "allowPublication",allow_promotion AS "allowPromotion",allow_draft_pr AS "allowDraftPR",auto_triage AS "autoTriage",retention_days AS "retentionDays" FROM projects WHERE workspace_id=$1 ORDER BY name,id`,
	"devices":      `SELECT id,name,platform,last_contact AS "lastContact",revoked,COALESCE((SELECT jsonb_agg(project_id ORDER BY project_id) FROM device_projects WHERE device_id=d.id),'[]'::jsonb) AS projects FROM devices d WHERE workspace_id=$1 ORDER BY name,id`,
	"decisions":    `SELECT d.id,d.project_id AS "projectId",question, jsonb_build_array('Resume dispatch','Keep paused') AS options, d.version, d.subject_version AS "subjectVersion", d.answer, d.request_id AS "requestId", CASE WHEN answer IS NOT NULL THEN 'answered' WHEN expires_at<=now() OR p.version<>subject_version THEN 'stale' ELSE 'pending' END AS status, expires_at AS "expiresAt" FROM decisions d JOIN projects p ON p.id=d.project_id WHERE d.workspace_id=$1 ORDER BY d.created_at DESC LIMIT 200`,
	"messages":     `SELECT id,project_id AS "projectId",body,'blocked' AS status,'Hosted model authorization pending' AS reason,created_at AS "createdAt" FROM (SELECT * FROM manager_messages WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 200) m ORDER BY created_at,id`,
	"events":       `SELECT seq,project_id AS "projectId",kind,subject_id AS "subjectId",created_at AS "createdAt",actor_source AS "actorSource",actor_id AS "actorId",reason,policy_version AS "policyVersion",subject_version AS "subjectVersion",evidence_ref AS "evidenceRef" FROM events WHERE workspace_id=$1 ORDER BY seq DESC LIMIT 150`,
	"attempts":     `SELECT a.id,a.project_id AS "projectId",a.device_id AS "deviceId",d.name AS machine,(SELECT jsonb_build_object('id',id,'operation',operation,'status',CASE WHEN status IN('requested','received') AND expires_at<=now() THEN 'expired' ELSE status END) FROM controls c WHERE c.workspace_id=a.workspace_id AND c.attempt_id=a.id ORDER BY created_at DESC LIMIT 1) AS control,p.repository,a.issue_number AS "issueNumber",a.base_sha AS "baseSha",a.candidate_sha AS "candidateSha",a.phase,a.state,a.reason_code AS "reasonCode",a.version,a.evidence,(SELECT jsonb_build_object('digest',digest,'baseSha',base_sha,'candidateSha',candidate_sha,'sharedAt',created_at) FROM shared_diffs f WHERE f.workspace_id=a.workspace_id AND f.attempt_id=a.id AND f.candidate_sha=a.candidate_sha AND f.version=a.version) AS "sharedDiff",a.updated_at AS "updatedAt",CASE WHEN d.revoked OR d.last_contact < now()-interval '2 minutes' THEN 'disconnected' ELSE 'connected' END AS connection FROM attempt_reports a JOIN devices d ON d.id=a.device_id JOIN projects p ON p.id=a.project_id WHERE a.workspace_id=$1 ORDER BY a.updated_at DESC LIMIT 500`,
	"tickets": `SELECT * FROM (
	SELECT x.incident_id AS id,x.project_id AS "projectId",x.number,p.repository,i.observed AS title,x.github_state AS state,'incident' AS source,
	  x.reason, x.checked_at AS "checkedAt" FROM incident_issues x JOIN incidents i ON i.id=x.incident_id JOIN projects p ON p.id=x.project_id WHERE x.workspace_id=$1 AND x.number IS NOT NULL
	UNION ALL
	SELECT 'github-'||z.repository_id||'-'||z.github_id,z.project_id,z.number,p.repository,coalesce(nullif(z.title,''),'Issue #'||z.number),
	  CASE WHEN NOT m.active OR z.availability<>'current' OR z.checked_at IS NULL OR z.checked_at<=now()-interval '20 minutes' THEN 'unknown' ELSE z.source_state END,'plan',
	  CASE WHEN NOT m.active THEN 'Repository mapping disabled' WHEN z.availability<>'current' THEN z.reason
	       WHEN z.checked_at<=now()-interval '20 minutes' THEN 'GitHub state needs a fresh read'
	       WHEN NOT EXISTS(SELECT FROM plan_issue_bindings b JOIN plan_versions v ON v.id=b.plan_id WHERE (b.workspace_id,b.project_id,b.number,b.generation)=(z.workspace_id,z.project_id,z.number,z.generation) AND v.id=(SELECT id FROM plan_versions WHERE workspace_id=z.workspace_id AND project_id=z.project_id AND approved_at IS NOT NULL ORDER BY version DESC LIMIT 1)) THEN 'A fresh reviewed plan is required' ELSE '' END,z.checked_at
	FROM planned_issue_state z JOIN projects p ON (p.workspace_id,p.id)=(z.workspace_id,z.project_id) JOIN github_mappings m ON (m.workspace_id,m.project_id,m.repository_id,m.installation_id)=(z.workspace_id,z.project_id,z.repository_id,z.installation_id) WHERE z.workspace_id=$1) all_tickets ORDER BY "checkedAt" DESC NULLS LAST,id LIMIT 500`,
}

func (s *Server) state(w http.ResponseWriter, r *http.Request, ws string) error {
	data := object{"capabilities": object{"hostedInference": false, "reason": inferenceReason}, "plans": []string{}, "pullRequests": []string{}}
	var name string
	var tickets, turns, limitsV, dispatchV int
	var stopped bool
	if err := s.DB.QueryRowContext(r.Context(), "SELECT name,ticket_limit,turn_limit,limits_version,stopped,dispatch_version FROM workspaces WHERE id=$1", ws).Scan(&name, &tickets, &turns, &limitsV, &stopped, &dispatchV); err != nil {
		return err
	}
	data["limits"] = object{"tickets": tickets, "turns": turns, "version": limitsV}
	data["workspace"] = object{"id": ws, "name": name, "stopped": stopped, "dispatchVersion": dispatchV}
	for key, q := range views {
		var b json.RawMessage
		if err := s.DB.QueryRowContext(r.Context(), "SELECT COALESCE(json_agg(t),'[]') FROM ("+q+") t", ws).Scan(&b); err != nil {
			return err
		}
		data[key] = b
	}
	plans, err := s.listPlans(r.Context(), ws)
	if err != nil {
		return err
	}
	data["plans"] = plans
	pulls, err := s.pulls().List(r.Context(), ws)
	if err != nil {
		return err
	}
	data["pullRequests"] = pulls
	drafts, err := s.pulls().Drafts(r.Context(), ws)
	if err != nil {
		return err
	}
	data["draftPRs"] = drafts
	list, err := s.Incidents.List(r.Context(), ws)
	if err != nil {
		return err
	}
	data["incidents"] = list
	data["updatedAt"] = time.Now().UTC()
	return send(w, 200, data)
}
func (s *Server) createProject(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct{ Name, Repository string }
	if err := decode(w, r, &b); err != nil {
		return err
	}
	b.Name = strings.TrimSpace(b.Name)
	b.Repository = strings.ToLower(strings.TrimSpace(b.Repository))
	if b.Name == "" || len(b.Name) > 100 || !repoPattern.MatchString(b.Repository) || strings.HasSuffix(b.Repository, "/.") || strings.HasSuffix(b.Repository, "/..") {
		return bad("Enter a name and a GitHub owner/repository")
	}
	id := random(16)
	ctx := r.Context()
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO projects(id,workspace_id,name,repository,paused) SELECT $1,$2,$3,$4,stopped FROM workspaces WHERE id=$2 FOR SHARE", id, ws, b.Name, b.Repository); err != nil {
			return err
		}
		return event(ctx, tx, ws, id, "project.created", id)
	})
	if err != nil {
		return err
	}
	return send(w, 201, object{"id": id})
}
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		Paused  *bool
		Version int64
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if b.Paused == nil || b.Version < 1 {
		return bad("Provide paused and version")
	}
	p := r.PathValue("id")
	ctx := r.Context()
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
		return send(w, 202, object{"decisionId": decision})
	}
	return send(w, 200, object{"paused": *b.Paused})
}

type decisionAnswer struct {
	Answer    string `json:"answer"`
	RequestID string `json:"requestId"`
	Version   int64  `json:"version"`
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request, ws string) error {
	var b decisionAnswer
	if err := decode(w, r, &b); err != nil {
		return err
	}
	id := r.PathValue("id")
	if err := s.applyAnswer(r.Context(), ws, id, b); err != nil {
		return err
	}
	return send(w, 200, object{"id": id, "answer": b.Answer})
}
func (s *Server) applyAnswer(ctx context.Context, ws, id string, b decisionAnswer) error {
	if (b.Answer != "Resume dispatch" && b.Answer != "Keep paused") || !keyPattern.MatchString(b.RequestID) || b.Version < 1 {
		return bad("Provide a valid choice, version, and request ID")
	}
	return transaction(ctx, s.DB, func(tx *sql.Tx) error {
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
	})
}

type messageInput struct {
	ProjectID string `json:"projectId"`
	RequestID string `json:"requestId"`
	Body      string `json:"body"`
}

func (s *Server) message(w http.ResponseWriter, r *http.Request, ws string) error {
	var b messageInput
	if err := decode(w, r, &b); err != nil {
		return err
	}
	out, err := s.saveMessage(r.Context(), ws, b)
	if err != nil {
		return err
	}
	return send(w, 202, out)
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
func (s *Server) report(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct{ ProjectID, RequestID, Observed, Expected, Steps string }
	if err := decode(w, r, &b); err != nil {
		return err
	}
	id, err := s.Incidents.Report(r.Context(), ws, b.ProjectID, b.Observed, b.Expected, b.Steps, b.RequestID)
	if err != nil {
		return incidentError(err)
	}
	return send(w, 201, object{"id": id})
}
func (s *Server) promote(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct{ Version int64 }
	if err := decode(w, r, &b); err != nil {
		return err
	}
	err := s.Incidents.Promote(r.Context(), ws, r.PathValue("project"), r.PathValue("id"), b.Version)
	if err != nil {
		return incidentError(err)
	}
	return send(w, 202, object{"status": "accepted"})
}
