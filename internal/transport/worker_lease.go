// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

// Revalidation observes existing ownership and never acquires or releases
// capacity. The admission sequence fences delayed callers from prior stages.
func (s *Server) workerLease(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var a protocol.WorkerLease
	if err := decode(w, r, &a); err != nil {
		return err
	}
	if err := compatible(a.Protocol); err != nil {
		return err
	}
	if !keyPattern.MatchString(a.Attempt) || a.Issue < 1 || !shaPattern.MatchString(a.Base) || a.Sequence < 1 || a.SubjectVersion < 1 {
		return bad("Invalid worker lease subject")
	}
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var plan string
	var seq int64
	var allowed, inTurn bool
	var controlID sql.NullString
	var operation sql.NullString
	var until sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT coalesce(a.plan_id,''),a.sequence,a.active AND NOT w.stopped AND NOT p.paused AND (NOT p.require_plan OR a.plan_id IS NOT NULL) AND a.plan_id IS NOT DISTINCT FROM (SELECT id FROM plan_versions WHERE workspace_id=w.id AND project_id=p.id AND approved_at IS NOT NULL ORDER BY version DESC LIMIT 1),a.in_turn,c.id,c.operation,c.expires_at
 FROM admission a JOIN devices d ON d.id=a.device_id AND d.workspace_id=a.workspace_id JOIN workspaces w ON w.id=a.workspace_id
 JOIN projects p ON p.id=a.project_id AND p.workspace_id=a.workspace_id JOIN device_projects m ON m.project_id=p.id AND m.device_id=d.id AND m.workspace_id=w.id
 LEFT JOIN LATERAL (SELECT id,operation,expires_at FROM controls WHERE workspace_id=w.id AND attempt_id=a.attempt_id AND project_id=p.id AND device_id=d.id AND subject_version=$8 AND status IN ('requested','received') AND expires_at>now() ORDER BY created_at DESC LIMIT 1) c ON true
 WHERE d.token_hash=$1 AND NOT d.revoked AND w.id=$2 AND d.id=$3 AND p.id=$4 AND a.attempt_id=$5 AND a.issue_number=$6 AND a.base_sha=$7`, hash(token), a.Workspace, a.Device, a.Project, a.Attempt, a.Issue, a.Base, a.SubjectVersion).Scan(&plan, &seq, &allowed, &inTurn, &controlID, &operation, &until)
	if err == sql.ErrNoRows {
		return denied
	}
	if err != nil {
		return err
	}
	if seq != a.Sequence || inTurn != a.Turn {
		return conflict("Worker admission sequence or stage changed")
	}
	grant := protocol.WorkerGrant{Sequence: seq}
	if controlID.Valid {
		grant.Control = &protocol.Control{ID: controlID.String, WorkspaceID: a.Workspace, DeviceID: a.Device, ProjectID: a.Project, AttemptID: a.Attempt, Operation: operation.String, SubjectVersion: a.SubjectVersion, ExpiresAt: until.Time}
		return send(w, 200, grant)
	}
	if !allowed {
		return conflict("Worker authority suspended")
	}
	if err = checkPlannedIssue(ctx, tx, a.Workspace, a.Project, plan, a.Issue, false); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	grant.ValidSeconds = protocol.WorkerLeaseSeconds
	return send(w, 200, grant)
}
