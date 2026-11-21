// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"net/http"
	"slices"
)

func (s *Server) control(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		Operation, RequestID string
		Version              int64
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if !keyPattern.MatchString(b.RequestID) || (b.Operation != "pause" && b.Operation != "stop") || b.Version < 1 {
		return bad("Choose a control and the current attempt version")
	}
	ctx := r.Context()
	attempt := r.PathValue("id")
	var id, status string
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var p, device, state string
		var version int64
		err := tx.QueryRowContext(ctx, "SELECT project_id,device_id,state,version FROM attempt_reports WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, attempt).Scan(&p, &device, &state, &version)
		if err != nil {
			return err
		}
		var existingOp, existingAttempt string
		var existingVersion int64
		err = tx.QueryRowContext(ctx, "SELECT id,status,operation,attempt_id,subject_version FROM controls WHERE workspace_id=$1 AND request_id=$2", ws, b.RequestID).Scan(&id, &status, &existingOp, &existingAttempt, &existingVersion)
		if err == nil {
			if existingOp != b.Operation || existingAttempt != attempt || existingVersion != b.Version {
				return conflict("Request ID already used")
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		if version != b.Version || slices.Contains([]string{"completed", "failed", "cancelled"}, state) {
			return conflict("Attempt changed or already ended")
		}
		var revoked bool
		if err = tx.QueryRowContext(ctx, "SELECT revoked FROM devices WHERE workspace_id=$1 AND id=$2", ws, device).Scan(&revoked); err != nil {
			return err
		}
		if revoked {
			return conflict("The owning computer was revoked")
		}
		if _, err = tx.ExecContext(ctx, "UPDATE controls SET status='expired' WHERE workspace_id=$1 AND attempt_id=$2 AND status IN ('requested','received') AND expires_at<=now()", ws, attempt); err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, "SELECT id,status,operation FROM controls WHERE workspace_id=$1 AND attempt_id=$2 AND status IN ('requested','received') FOR UPDATE", ws, attempt).Scan(&id, &status, &existingOp)
		if err == nil {
			if b.Operation == existingOp {
				return nil
			}
			if existingOp == "stop" {
				return conflict("Stop already requested")
			}
			if _, err = tx.ExecContext(ctx, "UPDATE controls SET status='superseded' WHERE id=$1", id); err != nil {
				return err
			}
		} else if err != sql.ErrNoRows {
			return err
		}
		id = random(16)
		status = "requested"
		if _, err = tx.ExecContext(ctx, "INSERT INTO controls(id,workspace_id,project_id,attempt_id,device_id,operation,subject_version,status,request_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", id, ws, p, attempt, device, b.Operation, version, status, b.RequestID); err != nil {
			return err
		}
		return event(ctx, tx, ws, p, "control."+b.Operation+"_requested", attempt)
	})
	if err != nil {
		return err
	}
	return send(w, 202, object{"id": id, "status": status})
}
func (s *Server) controls(ctx context.Context, tx *sql.Tx, ws, device string, acks []protocol.Ack) ([]protocol.Control, error) {
	for _, ack := range acks {
		if !keyPattern.MatchString(ack.ID) || !slices.Contains([]string{"received", "completed", "unavailable"}, ack.Status) {
			return nil, bad("Invalid command acknowledgment")
		}
		var current, p, id string
		err := tx.QueryRowContext(ctx, "SELECT status,project_id,attempt_id FROM controls WHERE id=$1 AND workspace_id=$2 AND device_id=$3 FOR UPDATE", ack.ID, ws, device).Scan(&current, &p, &id)
		if err == sql.ErrNoRows {
			return nil, denied
		}
		if err != nil {
			return nil, err
		}
		if current == ack.Status || current != "received" && current != "requested" {
			continue
		}
		if _, err = tx.ExecContext(ctx, "UPDATE controls SET status=$1 WHERE id=$2", ack.Status, ack.ID); err != nil {
			return nil, err
		}
		if err = event(ctx, tx, ws, p, "control."+ack.Status, id); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,workspace_id,project_id,device_id,attempt_id,operation,subject_version,expires_at FROM controls WHERE workspace_id=$1 AND device_id=$2 AND status IN ('requested','received') AND expires_at>now() ORDER BY created_at,id LIMIT 64", ws, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Control{}
	for rows.Next() {
		var c protocol.Control
		if err = rows.Scan(&c.ID, &c.WorkspaceID, &c.ProjectID, &c.DeviceID, &c.AttemptID, &c.Operation, &c.SubjectVersion, &c.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
