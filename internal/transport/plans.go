// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"net/http"
	"slices"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
)

func (s *Server) proposePlan(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		ProjectID string
		Version   int64
		Plan      domain.Plan
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	order, err := b.Plan.Order()
	if err != nil {
		return bad(err.Error())
	}
	id := random(16)
	ctx := r.Context()
	err = transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var v int64
		if err := tx.QueryRowContext(ctx, "SELECT version FROM projects WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, b.ProjectID).Scan(&v); err != nil {
			return err
		}
		if v != b.Version {
			return conflict("Project changed; refresh before proposing a new plan")
		}
		var previous int
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(version),0) FROM plan_versions WHERE project_id=$1", b.ProjectID).Scan(&previous); err != nil {
			return err
		}
		doc, _ := json.Marshal(b.Plan)
		sorted, _ := json.Marshal(order)
		if _, err := tx.ExecContext(ctx, "INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order) VALUES($1,$2,$3,$4,$5,$6)", id, ws, b.ProjectID, previous+1, doc, sorted); err != nil {
			return err
		}
		if err := planning.BindIssues(ctx, tx, ws, b.ProjectID, id, b.Plan); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE projects SET version=version+1 WHERE id=$1", b.ProjectID); err != nil {
			return err
		}
		return event(ctx, tx, ws, b.ProjectID, "plan.proposed", id)
	})
	if err != nil {
		return err
	}
	return send(w, 201, object{"id": id, "order": order})
}
func (s *Server) approvePlan(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct{ Version int64 }
	if err := decode(w, r, &b); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var p string
		var v, planVersion int64
		var approved, current bool
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT p.id,p.version,v.approved_at IS NOT NULL,v.version=(SELECT max(version) FROM plan_versions WHERE project_id=p.id),v.document,v.version FROM plan_versions v JOIN projects p ON p.id=v.project_id WHERE v.workspace_id=$1 AND v.id=$2 FOR UPDATE OF p,v`, ws, id).Scan(&p, &v, &approved, &current, &raw, &planVersion)
		if err != nil {
			return err
		}
		if !current || v != b.Version {
			return conflict("Scope changed. Review the latest proposal.")
		}
		var doc domain.Plan
		if err = json.Unmarshal(raw, &doc); err != nil {
			return err
		}
		for _, ticket := range doc.Tickets {
			if err = checkPlannedIssue(ctx, tx, ws, p, id, ticket.Number, true); err != nil {
				return err
			}
		}
		if approved {
			return nil
		}
		var reserved bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT FROM admission WHERE project_id=$1 AND owns_issue)", p).Scan(&reserved); err != nil {
			return err
		}
		if reserved {
			return conflict("Reconcile owned attempts before replacing approved scope")
		}
		if _, err = tx.ExecContext(ctx, "UPDATE plan_versions SET approved_at=now() WHERE id=$1", id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE projects SET version=version+1 WHERE id=$1", p); err != nil {
			return err
		}
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Current plan and dependency bindings approved", SubjectVersion: planVersion})
		return event(ctx, tx, ws, p, "plan.approved", id)
	})
	if err != nil {
		return err
	}
	return send(w, 200, object{"id": id, "status": "approved"})
}
func (s *Server) attestPrerequisite(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		Number            int
		Revision, Summary string
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if b.Number <= 0 || !shaPattern.MatchString(b.Revision) || strings.TrimSpace(b.Summary) == "" || len(b.Summary) > 1000 {
		return bad("Supply the verified integration commit and acceptance evidence")
	}
	id := r.PathValue("id")
	ctx := r.Context()
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var project string
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT v.project_id,v.document FROM plan_versions v JOIN projects p ON p.id=v.project_id WHERE v.id=$1 AND v.workspace_id=$2 AND v.approved_at IS NOT NULL AND NOT EXISTS(SELECT FROM plan_versions newer WHERE newer.project_id=v.project_id AND newer.approved_at IS NOT NULL AND newer.version>v.version) FOR UPDATE OF p`, id, ws).Scan(&project, &raw)
		if err != nil {
			return err
		}
		var plan domain.Plan
		if err = json.Unmarshal(raw, &plan); err != nil {
			return err
		}
		if err = checkPlannedIssue(ctx, tx, ws, project, id, b.Number, true); err != nil {
			return err
		}
		if !slices.ContainsFunc(plan.Tickets, func(t domain.Ticket) bool { return t.Number == b.Number }) {
			return bad("Issue is outside this plan")
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO prerequisite_evidence(workspace_id,project_id,plan_id,issue_number,revision,summary) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, ws, project, id, b.Number, b.Revision, incidents.Redact(b.Summary))
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return conflict("Evidence already recorded")
		}
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Prerequisite evidence recorded for the selected plan", EvidenceRef: b.Revision})
		return event(ctx, tx, ws, project, "prerequisite.attested", id)
	})
	if err != nil {
		return err
	}
	return send(w, 201, object{"accepted": true})
}

func checkPlannedIssue(ctx context.Context, tx *sql.Tx, ws, p, plan string, number int, allowClosed bool) error {
	err := planning.CheckIssue(ctx, tx, ws, p, plan, number, allowClosed)
	if errors.Is(err, planning.ErrIssueDrift) {
		return conflict(err.Error())
	}
	return err
}
