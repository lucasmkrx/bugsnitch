// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lucasmkrx/bugsnitch/internal/approvalpolicy"
	"regexp"
	"strconv"
)

var evidenceSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Server) labelHistoricalCase(call *domainCall) error {
	n, err := strconv.ParseInt(call.Number, 10, 32)
	if err != nil || n < 1 {
		return bad("Invalid pull request")
	}
	var b struct {
		Label, Cohort, Reference, Head, Base, Target string
		Evidence                                     *approvalpolicy.Evidence
	}
	if err = call.decode(&b); err != nil {
		return err
	}
	if (b.Label != "clean" && b.Label != "regression" && b.Label != "revert") || (b.Cohort != "holdout" && b.Cohort != "calibration") || !evidenceSHA256.MatchString(b.Reference) || !shaPattern.MatchString(b.Base) || !shaPattern.MatchString(b.Head) {
		return bad("Choose an outcome, evaluation cohort and evidence SHA-256")
	}
	if b.Evidence != nil && (b.Evidence.Head != b.Head || b.Evidence.Base != b.Base || !evidenceSHA256.MatchString(b.Evidence.Reference) || len(b.Evidence.Checks) > 64 || len(b.Evidence.Approvers) > 64) {
		return bad("Historical review evidence must match the selected revisions and include its own SHA-256 reference")
	}
	evidence, err := json.Marshal(b.Evidence)
	if err != nil || len(evidence) > 16384 {
		return bad("Historical review evidence is too large")
	}
	err = transaction(call.ctx, s.DB, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(call.ctx, `INSERT INTO review_case_labels(workspace_id,project_id,pull_number,head,base,target,label,cohort,reference,actor,evidence)
      SELECT s.workspace_id,s.project_id,s.pull_number,s.head,s.base,s.target,$7,$8,$9,w.account_id,$10::jsonb FROM review_subjects s JOIN workspaces w ON w.id=s.workspace_id
      WHERE s.workspace_id=$1 AND s.project_id=$2 AND s.pull_number=$3 AND s.head=$4 AND s.base=$5 AND s.target=$6
      ON CONFLICT DO NOTHING`, call.Workspace, call.Project, n, b.Head, b.Base, b.Target, b.Label, b.Cohort, b.Reference, string(evidence))
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			var same bool
			if err = tx.QueryRowContext(call.ctx, `SELECT EXISTS(SELECT FROM review_case_labels WHERE workspace_id=$1 AND project_id=$2 AND pull_number=$3 AND head=$4 AND base=$5 AND target=$6 AND label=$7 AND cohort=$8 AND reference=$9 AND evidence=$10::jsonb)`, call.Workspace, call.Project, n, b.Head, b.Base, b.Target, b.Label, b.Cohort, b.Reference, string(evidence)).Scan(&same); err != nil {
				return err
			}
			if !same {
				return conflict("The selected pre-merge revision is missing or already labelled differently")
			}
			return nil
		}
		return event(call.ctx, tx, call.Workspace, call.Project, "review.case_labelled", call.Number)
	})
	if err != nil {
		return err
	}
	return call.reply(200, object{"label": b.Label})
}
func (s *Server) exportHistoricalCases(call *domainCall) error {
	// A fixed-build recurrence is a regression control for the reviewed fix.
	// Silence is never converted to a clean label. Without an independently
	// recorded check identity the exported case has no approval evidence.
	rows, err := s.DB.QueryContext(call.ctx, `WITH recurrence AS (
 SELECT DISTINCT ON (o.number) pre.*, 'regression' AS label,'holdout' AS cohort,'deployment-hold/'||h.id AS reference
 FROM deployment_holds h JOIN deployment_monitors m ON m.id=h.monitor_id JOIN pull_observations o ON (o.workspace_id,o.project_id)=(m.workspace_id,m.project_id)
 JOIN incident_issues issue ON (issue.workspace_id,issue.project_id,issue.incident_id,issue.number)=(o.workspace_id,o.project_id,m.incident_id,o.issue_number)
 JOIN LATERAL (SELECT s.* FROM review_subjects s WHERE (s.workspace_id,s.project_id,s.pull_number,s.head,s.target)=(o.workspace_id,o.project_id,o.number,o.snapshot->>'head',o.snapshot->>'target') AND s.captured_at<=h.created_at ORDER BY s.captured_at DESC LIMIT 1) pre ON true
 WHERE o.workspace_id=$1 AND o.project_id=$2 AND o.snapshot->>'state'='MERGED' AND o.snapshot->>'mergedSha'=m.candidate AND h.reason='current-build recurrence'
 AND issue.status='linked' AND (issue.installation_id,issue.repository_id)=(m.installation_id,m.repository_id)
 AND (m.pull_number IS NULL OR m.pull_number=o.number)
 ORDER BY o.number,h.created_at DESC,h.id DESC
 ), cases AS (
 SELECT pull_number,head,base,target,label,cohort,reference,evidence,1 AS precedence FROM review_case_labels WHERE workspace_id=$1 AND project_id=$2
 UNION ALL SELECT r.pull_number,r.head,r.base,r.target,r.label,coalesce(l.cohort,r.cohort),r.reference,coalesce(l.evidence,'null'::jsonb),0 AS precedence
 FROM recurrence r LEFT JOIN review_case_labels l USING(workspace_id,project_id,pull_number,head,base,target))
 SELECT DISTINCT ON(head,base) pull_number,head,base,target,label,cohort,reference,evidence FROM cases ORDER BY head,base,precedence,pull_number,target LIMIT 501`, call.Workspace, call.Project)
	if err != nil {
		return err
	}
	defer rows.Close()
	cases := make([]object, 0)
	for rows.Next() {
		var n int
		var evidence json.RawMessage
		var head, base, target, label, cohort, ref string
		if err = rows.Scan(&n, &head, &base, &target, &label, &cohort, &ref, &evidence); err != nil {
			return err
		}
		identity := sha256.Sum256([]byte(target + " " + base + " " + head))
		entry := object{"id": fmt.Sprintf("pr-%d-%x", n, identity[:12]), "base": base, "head": head, "label": label, "cohort": cohort, "labelReference": ref}
		if len(evidence) > 0 && string(evidence) != "null" {
			entry["evidence"] = evidence
		}
		cases = append(cases, entry)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(cases) > 500 {
		return conflict("Export exceeds the limit of 500 cases")
	}
	return call.reply(200, object{"schema": 1, "cases": cases})
}

func (s *Server) reviewSubjects(call *domainCall) error {
	rows, err := s.DB.QueryContext(call.ctx, `SELECT s.pull_number,s.base,s.head,s.target,l.label FROM review_subjects s LEFT JOIN review_case_labels l
  USING(workspace_id,project_id,pull_number,head,base,target) WHERE s.workspace_id=$1 AND s.project_id=$2
  ORDER BY s.captured_at DESC,s.pull_number,s.head,s.base,s.target LIMIT 200`, call.Workspace, call.Project)
	if err != nil {
		return err
	}
	defer rows.Close()
	subjects := make([]object, 0)
	for rows.Next() {
		var n int64
		var base, head, target string
		var label sql.NullString
		if err = rows.Scan(&n, &base, &head, &target, &label); err != nil {
			return err
		}
		subjects = append(subjects, object{"number": n, "base": base, "head": head, "target": target, "label": label.String})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return call.reply(200, subjects)
}
