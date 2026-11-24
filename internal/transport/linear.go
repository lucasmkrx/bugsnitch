// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/linear"
)

func (s *Server) linkLinear(call *domainCall) error {
	if s.Linear == nil {
		return problem{503, "Linear issue linking requires a configured Linear API key."}
	}
	number, err := strconv.ParseInt(call.Number, 10, 32)
	var b struct{ IssueID string }
	if decodeErr := call.decode(&b); decodeErr != nil {
		return decodeErr
	}
	b.IssueID = strings.TrimSpace(b.IssueID)
	if err != nil || number < 1 || !linear.ValidIssueID(b.IssueID) {
		return bad("Choose a canonical issue and a Linear issue identifier")
	}
	ctx, ws, project := call.ctx, call.Workspace, call.Project
	const canonical = `SELECT EXISTS (SELECT FROM planned_issue_state WHERE workspace_id=$1 AND project_id=$2 AND number=$3)
	    OR EXISTS (SELECT FROM incident_issues WHERE workspace_id=$1 AND project_id=$2 AND number=$3)`
	var known bool
	if err := s.DB.QueryRowContext(ctx, canonical, ws, project, number).Scan(&known); err != nil {
		return err
	}
	if !known {
		return problem{404, "Canonical GitHub issue unavailable"}
	}
	issue, err := s.Linear.Issue(ctx, b.IssueID)
	if errors.Is(err, linear.Unavailable) {
		return problem{502, "Linear issue could not be read. Check the issue and the API key's team permissions."}
	}
	if err != nil {
		return err
	}
	bounded := func(value string, limit int) string {
		text := []rune(incidents.Redact(value))
		return string(text[:min(len(text), limit)])
	}
	err = transaction(ctx, s.DB, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO linear_issue_links(
		    workspace_id,project_id,issue_number,linear_id,identifier,title,url,state_name,state_type,source_updated_at)
		    VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		    ON CONFLICT(workspace_id,project_id,issue_number) DO UPDATE
		    SET identifier=excluded.identifier,title=excluded.title,url=excluded.url,
		        state_name=excluded.state_name,state_type=excluded.state_type,
		        source_updated_at=excluded.source_updated_at,checked_at=now()
		    WHERE linear_issue_links.linear_id=excluded.linear_id
		      AND linear_issue_links.source_updated_at<=excluded.source_updated_at`,
			ws, project, number, issue.ID, issue.Identifier, bounded(issue.Title, 240), issue.URL,
			bounded(issue.State.Name, 100), bounded(issue.State.Type, 32), issue.UpdatedAt)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return conflict("Linear issue binding or source revision changed")
		}
		return audit.Record(audit.WithDetails(ctx, audit.Details{Reason: "Linear issue read for the linked GitHub ticket", EvidenceRef: issue.ID}), tx, ws, project, "linear.issue_read", call.Number)
	})
	if err != nil {
		return err
	}
	return call.reply(200, object{"identifier": issue.Identifier})
}
