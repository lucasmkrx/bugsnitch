// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func (s *Server) shareBrowserQA(c *domainCall) error {
	var in struct {
		Version int64                    `json:"version"`
		Report  protocol.BrowserQAReport `json:"report"`
	}
	if err := c.decode(&in); err != nil {
		return err
	}
	if in.Version < 1 {
		return bad("Provide the current incident version.")
	}
	r := in.Report
	if err := r.Validate(); err != nil {
		return bad(err.Error())
	}
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	err = transaction(c.ctx, s.DB, func(tx *sql.Tx) error {
		var current int64
		if err := tx.QueryRowContext(c.ctx, `SELECT version FROM incidents WHERE workspace_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, c.Workspace, c.Project, c.ID).Scan(&current); err != nil {
			return err
		}
		var issue int
		if err := tx.QueryRowContext(c.ctx, `SELECT COALESCE(number,0) FROM incident_issues WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3 AND status='linked'`, c.Workspace, c.Project, c.ID).Scan(&issue); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if issue != r.IssueNumber || current != in.Version {
			return conflict("Incident or linked issue changed. Refresh the incident and review the report.")
		}
		var ready int
		err := tx.QueryRowContext(c.ctx, `SELECT 1 FROM attempt_reports WHERE workspace_id=$1 AND project_id=$2 AND id=$3 AND issue_number=$4 AND base_sha=$5 AND candidate_sha=$6 FOR SHARE`, c.Workspace, c.Project, r.AttemptID, issue, r.Before.Revision, r.After.Revision).Scan(&ready)
		if errors.Is(err, sql.ErrNoRows) {
			return conflict("The report does not match a synced attempt for this incident. Run bugsnitch sync on the original computer and check the candidate revision.")
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(c.ctx, `INSERT INTO incident_browser_qa AS q (workspace_id,project_id,incident_id,attempt_id,candidate_sha,checked_at,digest,report)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(incident_id,attempt_id,candidate_sha) DO UPDATE SET
			checked_at=EXCLUDED.checked_at,digest=EXCLUDED.digest,report=EXCLUDED.report,created_at=now()
			WHERE q.checked_at < EXCLUDED.checked_at`, c.Workspace, c.Project, c.ID, r.AttemptID, r.After.Revision, r.CheckedAt, digest, string(body))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var existing string
			if err := tx.QueryRowContext(c.ctx, `SELECT digest FROM incident_browser_qa WHERE incident_id=$1 AND attempt_id=$2 AND candidate_sha=$3`, c.ID, r.AttemptID, r.After.Revision).Scan(&existing); err != nil {
				return err
			}
			if existing != digest {
				return conflict("A newer browser QA report is already shared for this candidate.")
			}
			return nil
		}
		ctx := audit.WithDetails(c.ctx, audit.Details{SubjectVersion: current, EvidenceRef: digest, Reason: "Shared browser QA for a synced candidate"})
		return event(ctx, tx, c.Workspace, c.Project, "incident.browser_qa_shared", c.ID)
	})
	if err != nil {
		return err
	}
	return c.reply(200, object{"digest": digest})
}

func (s *Server) incidentBrowserQA(c *domainCall) error {
	var report json.RawMessage
	err := s.DB.QueryRowContext(c.ctx, `SELECT coalesce(q.report,'null') FROM incidents i
		JOIN projects p ON (p.workspace_id,p.id)=(i.workspace_id,i.project_id)
		LEFT JOIN LATERAL (SELECT e.report FROM incident_browser_qa e WHERE e.incident_id=i.id
		 AND e.created_at>=now()-p.retention_days*interval '1 day' ORDER BY e.checked_at DESC,e.attempt_id DESC LIMIT 1) q ON true
		WHERE i.workspace_id=$1 AND i.project_id=$2 AND i.id=$3`, c.Workspace, c.Project, c.ID).Scan(&report)
	if err != nil {
		return err
	}
	return c.reply(200, object{"report": report})
}

func (s *Server) pullBrowserQA(c *domainCall) error {
	number, err := strconv.ParseInt(c.Number, 10, 64)
	if err != nil || number < 1 {
		return bad("Invalid pull request number")
	}
	var report json.RawMessage
	err = s.DB.QueryRowContext(c.ctx, `SELECT coalesce(q.report,'null') FROM pull_observations o
		JOIN projects p ON (p.workspace_id,p.id)=(o.workspace_id,o.project_id)
		LEFT JOIN LATERAL (SELECT e.report FROM incident_issues i JOIN incident_browser_qa e ON e.incident_id=i.incident_id
		 WHERE (i.workspace_id,i.project_id,i.number)=(o.workspace_id,o.project_id,o.issue_number) AND i.status='linked'
		 AND e.candidate_sha=o.snapshot->>'head' AND e.created_at>=now()-p.retention_days*interval '1 day'
		 ORDER BY e.checked_at DESC,e.attempt_id DESC LIMIT 1) q ON true
		WHERE o.workspace_id=$1 AND o.project_id=$2 AND o.number=$3`, c.Workspace, c.Project, number).Scan(&report)
	if err != nil {
		return err
	}
	return c.reply(200, object{"report": report})
}
