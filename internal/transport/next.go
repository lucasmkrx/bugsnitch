// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
)

func (s *Server) nextIssue(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var in protocol.NextRequest
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if err := compatible(in.Protocol); err != nil {
		return err
	}
	rec, err := s.recommend(r.Context(), token, in.Project)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return send(w, 200, rec)
}

// A consistent snapshot can inform the owner. Only the separate admission
// transaction may reserve capacity, an issue, or its shared resources.
func (s *Server) recommend(ctx context.Context, token, p string) (protocol.Recommendation, error) {
	var out protocol.Recommendation
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var ws, d, repo string
	var paused, stopped, mapped bool
	var used, ticketLimit, turnLimit, deviceLimit int
	err = tx.QueryRowContext(ctx, `SELECT w.id,d.id,p.repository,p.paused,w.stopped,w.ticket_limit,w.turn_limit,d.turn_limit,
    EXISTS(SELECT FROM github_mappings g WHERE g.workspace_id=w.id AND g.project_id=p.id AND g.active),
    (SELECT count(*) FROM admission a WHERE a.workspace_id=w.id AND a.active)
    FROM device_projects m JOIN devices d ON (m.workspace_id,m.device_id)=(d.workspace_id,d.id) JOIN workspaces w ON w.id=d.workspace_id
    JOIN projects p ON (m.workspace_id,m.project_id)=(p.workspace_id,p.id)
    WHERE p.id=$1 AND NOT d.revoked AND d.token_hash=$2`, p, hash(token)).Scan(&ws, &d, &repo, &paused, &stopped, &ticketLimit, &turnLimit, &deviceLimit, &mapped, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return out, denied
	}
	if err != nil {
		return out, err
	}
	switch {
	case stopped:
		out.Reason = "Workspace dispatch is stopped"
	case paused:
		out.Reason = "Project dispatch is paused"
	case !mapped:
		out.Reason = "The project needs an active GitHub App repository mapping"
	case used >= ticketLimit:
		out.Reason = "All workspace implementation slots are reserved"
	}
	if out.Reason != "" {
		return out, nil
	}
	available, err := turns.Available(ctx, tx, ws, d, turnLimit, deviceLimit)
	if err != nil {
		return out, err
	}
	if !available {
		out.Reason = "All workspace or device model-turn slots are reserved"
		return out, nil
	}
	var plan string
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT id,document FROM plan_versions WHERE workspace_id=$1 AND project_id=$2 AND approved_at IS NOT NULL ORDER BY version DESC LIMIT 1", ws, p).Scan(&plan, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		out.Reason = "Approve a plan before requesting its next issue"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var doc domain.Plan
	if err = json.Unmarshal(raw, &doc); err != nil {
		return out, err
	}
	if _, err = doc.Order(); err != nil {
		return out, errors.New("invalid stored plan")
	}
	slices.SortFunc(doc.Tickets, func(a, b domain.Ticket) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return cmp.Compare(a.Number, b.Number)
	})
	for _, ticket := range doc.Tickets {
		next, err := recommendTicket(ctx, tx, ws, p, plan, repo, ticket)
		if err != nil {
			return out, err
		}
		if next.Selection != nil {
			return next, nil
		}
		if out.Reason == "" {
			out.Reason = fmt.Sprintf("No eligible issue. Highest-priority issue #%d: %s", ticket.Number, next.Reason)
		}
	}
	return out, nil
}

func recommendTicket(ctx context.Context, tx *sql.Tx, ws, p, plan, repo string, t domain.Ticket) (protocol.Recommendation, error) {
	var out protocol.Recommendation
	var prior, integrated bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT FROM admission WHERE workspace_id=$1 AND project_id=$2 AND issue_number=$3),
    EXISTS(SELECT FROM prerequisite_evidence WHERE workspace_id=$1 AND project_id=$2 AND issue_number=$3 AND plan_id=$4)`, ws, p, t.Number, plan).Scan(&prior, &integrated)
	if err != nil {
		return out, err
	}
	if prior {
		out.Reason = "an attempt already exists; inspect or explicitly retry it"
		return out, nil
	}
	if integrated {
		out.Reason = "the plan already records integration evidence"
		return out, nil
	}
	_, _, err = planEligibility(ctx, tx, ws, protocol.Admission{ProjectID: p, IssueNumber: t.Number, CriteriaHash: domain.CriteriaHash(t.Criteria)})
	var block problem
	if errors.As(err, &block) && block.code == 409 {
		out.Reason = block.message
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if len(t.Dependencies) > 0 {
		deps, err := json.Marshal(t.Dependencies)
		if err != nil {
			return out, err
		}
		var unbound bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT FROM jsonb_array_elements_text($4::jsonb) dep(value)
        WHERE NOT EXISTS(SELECT FROM plan_issue_bindings b JOIN planned_issue_state z USING(workspace_id,project_id,number,generation)
        WHERE b.workspace_id=$1 AND b.project_id=$2 AND b.plan_id=$3 AND b.number=dep.value::bigint))`, ws, p, plan, deps).Scan(&unbound)
		if err != nil {
			return out, err
		}
		if unbound {
			out.Reason = "a prerequisite lacks a verified canonical issue revision in this plan"
			return out, nil
		}
	}
	var title, revision, digest string
	var identity int64
	err = tx.QueryRowContext(ctx, `SELECT z.github_id,z.digest,z.title,z.selection_revision FROM planned_issue_state z
    JOIN plan_issue_bindings b USING (workspace_id,project_id,number,generation)
    WHERE b.plan_id=$1 AND z.workspace_id=$2 AND z.project_id=$3 AND z.number=$4`, plan, ws, p, t.Number).Scan(&identity, &digest, &title, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		out.Reason = "the approved plan lacks a verified canonical GitHub issue revision"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if revision == "" || len(digest) != 64 {
		out.Reason = "awaiting a canonical GitHub timestamp and a title suitable for sharing"
		return out, nil
	}
	for _, c := range t.Criteria {
		if incidents.Redact(c) != c {
			out.Reason = "the approved criteria require redaction before sharing"
			return out, nil
		}
	}
	out.Selection = &protocol.IssueSelection{GitHubID: identity, RequirementsDigest: digest, Schema: 1, Repository: repo, Number: t.Number, URL: fmt.Sprintf("https://github.com/%s/issues/%d", repo, t.Number), Title: title, State: "open", UpdatedAt: revision, Criteria: t.Criteria}
	return out, nil
}
