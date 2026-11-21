// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
	"net/http"
	"strings"
)

func (s *Server) generalTurn(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var a turns.Operation
	if err := decode(w, r, &a); err != nil {
		return err
	}
	if err := compatible(a.Protocol); err != nil {
		return err
	}
	if !a.Valid() {
		return bad("Invalid local model permit")
	}
	ctx := r.Context()
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var devLimit, wsLimit int
		err := tx.QueryRowContext(ctx, "SELECT turn_limit FROM devices WHERE id=$1 AND workspace_id=$2 AND token_hash=$3 AND NOT revoked FOR UPDATE", a.Device, a.Workspace, hash(token)).Scan(&devLimit)
		if err == sql.ErrNoRows {
			return denied
		}
		if err != nil {
			return err
		}
		var stopped bool
		if err = tx.QueryRowContext(ctx, "SELECT turn_limit,stopped FROM workspaces WHERE id=$1 FOR UPDATE", a.Workspace).Scan(&wsLimit, &stopped); err != nil {
			return err
		}
		if stopped && a.Version == 1 {
			return conflict("Workspace emergency stop is active")
		}
		if a.Project != "" {
			var paused bool
			err = tx.QueryRowContext(ctx, "SELECT p.paused FROM projects p JOIN device_projects m ON m.project_id=p.id AND m.workspace_id=p.workspace_id WHERE p.id=$1 AND p.workspace_id=$2 AND m.device_id=$3 FOR SHARE OF p", a.Project, a.Workspace, a.Device).Scan(&paused)
			if err == sql.ErrNoRows {
				return denied
			}
			if err != nil {
				return err
			}
			if paused && a.Version == 1 {
				return conflict("Project dispatch is paused")
			}
		}
		changed, err := turns.Apply(ctx, tx, a, wsLimit, devLimit)
		if errors.Is(err, turns.ErrCapacity) || errors.Is(err, turns.ErrStale) {
			return conflict(err.Error())
		}
		if errors.Is(err, turns.ErrSubject) {
			return denied
		}
		if err != nil {
			return err
		}
		if changed {
			kind := "local_model.started"
			if a.Version == 2 {
				kind = "local_model.ended"
			}
			principal := audit.WithActor(ctx, audit.Device, a.Device)
			recorded := audit.WithDetails(principal, audit.Details{Reason: "Enrolled device reconciled a local model permit", SubjectVersion: int64(a.Version)})
			return event(recorded, tx, a.Workspace, a.Project, kind, a.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return send(w, 200, turns.Receipt{ID: a.ID, Version: a.Version})
}
