// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"net/http"
)

func (s *Server) workspaceControl(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		Stopped   *bool
		RequestID string
		Version   int64
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if b.Stopped == nil || b.Version < 1 || !keyPattern.MatchString(b.RequestID) {
		return bad("Provide the operation, current version and request ID")
	}
	ctx := r.Context()
	var result int64
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var version int64
		var stopped bool
		err := tx.QueryRowContext(ctx, "SELECT stopped,dispatch_version FROM workspaces WHERE id=$1 FOR UPDATE", ws).Scan(&stopped, &version)
		if err != nil {
			return err
		}
		var expected int64
		var previous bool
		err = tx.QueryRowContext(ctx, "SELECT stopped,expected,result FROM workspace_controls WHERE workspace_id=$1 AND request_id=$2", ws, b.RequestID).Scan(&previous, &expected, &result)
		if err == nil {
			if expected != b.Version || previous != *b.Stopped {
				return conflict("Request already used for a different control")
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		if b.Version != version {
			return conflict("Workspace controls changed. Review the current state.")
		}
		result = version
		if stopped != *b.Stopped {
			result++
			if _, err = tx.ExecContext(ctx, "UPDATE workspaces SET stopped=$2,dispatch_version=$3 WHERE id=$1", ws, *b.Stopped, result); err != nil {
				return err
			}
			if *b.Stopped {
				if _, err = tx.ExecContext(ctx, "UPDATE projects SET paused=true,version=version+1 WHERE workspace_id=$1", ws); err != nil {
					return err
				}
				if err = ensureStops(ctx, tx, ws, ""); err != nil {
					return err
				}
			}
			kind := "workspace.dispatch_unlocked"
			if *b.Stopped {
				kind = "workspace.emergency_stop"
			}
			ctx = audit.WithDetails(ctx, audit.Details{Reason: "Workspace dispatch control accepted", SubjectVersion: result})
			if err = event(ctx, tx, ws, "", kind, ws); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO workspace_controls(workspace_id,request_id,stopped,expected,result) VALUES($1,$2,$3,$4,$5)", ws, b.RequestID, *b.Stopped, b.Version, result)
		return err
	})
	if err != nil {
		return err
	}
	return send(w, 200, object{"stopped": *b.Stopped, "version": result})
}

// Reconcile stop requests against the owning machine's latest observed version even after reconnect.
func ensureStops(ctx context.Context, tx *sql.Tx, ws, device string) error {
	_, err := tx.ExecContext(ctx, `UPDATE controls c SET status='superseded' FROM attempt_reports a WHERE a.id=c.attempt_id AND a.workspace_id=c.workspace_id AND c.workspace_id=$1 AND ($2='' OR c.device_id=$2) AND c.status IN('received','requested') AND (c.operation='pause' OR (c.status='requested' AND c.subject_version<>a.version))`, ws, device)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO controls(id,workspace_id,project_id,attempt_id,device_id,operation,subject_version,status,request_id)
 SELECT gen_random_uuid()::text,a.workspace_id,a.project_id,a.id,a.device_id,'stop',a.version,'requested','emergency_'||a.id||'_'||a.version||'_'||w.dispatch_version FROM attempt_reports a JOIN workspaces w ON w.id=a.workspace_id JOIN devices d ON d.id=a.device_id WHERE w.stopped AND a.workspace_id=$1 AND ($2='' OR a.device_id=$2) AND a.state IN('running','waiting') AND NOT d.revoked AND NOT EXISTS(SELECT FROM controls c WHERE c.workspace_id=a.workspace_id AND c.attempt_id=a.id AND c.status IN('received','requested'))
 ON CONFLICT DO NOTHING`, ws, device)
	return err
}
