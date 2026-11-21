// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"net/http"
	"strings"
)

func (s *Server) retention(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		Days    int
		Version int64
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if b.Days < 1 || b.Days > 3650 {
		return bad("Choose a retention period from 1 to 3650 days")
	}
	ctx := r.Context()
	p := r.PathValue("id")
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE projects SET retention_days=$1,version=version+1 WHERE workspace_id=$2 AND id=$3 AND version=$4", b.Days, ws, p, b.Version)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return conflict("Project unavailable or changed")
		}
		return event(ctx, tx, ws, p, "retention.changed", p)
	})
	if err != nil {
		return err
	}
	return send(w, 200, object{"days": b.Days})
}
func (s *Server) rotate(w http.ResponseWriter, r *http.Request) error {
	old, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(old) {
		return denied
	}
	var b struct {
		Protocol   int
		Credential string
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := compatible(b.Protocol); err != nil {
		return err
	}
	if b.Credential == old || !credPattern.MatchString(b.Credential) {
		return bad("Provide a fresh device credential")
	}
	current, next := hash(old), hash(b.Credential)
	ctx := r.Context()
	var id string
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var ws, stored string
		err := tx.QueryRowContext(ctx, "SELECT workspace_id,id,token_hash FROM devices WHERE NOT revoked AND (token_hash=$1 OR (previous_hash=$1 AND token_hash=$2 AND rotated_at>now()-interval '1 day')) FOR UPDATE", current, next).Scan(&ws, &id, &stored)
		if err == sql.ErrNoRows {
			return denied
		}
		if err != nil {
			return err
		}
		if stored == next {
			return nil
		}
		if _, err = tx.ExecContext(ctx, "UPDATE devices SET previous_hash=token_hash,token_hash=$1,rotated_at=now() WHERE id=$2", next, id); err != nil {
			return err
		}
		principal := audit.WithActor(ctx, audit.Device, id)
		recorded := audit.WithDetails(principal, audit.Details{Reason: "Device authenticated with its previous credential"})
		return event(recorded, tx, ws, "", "device.rotated", id)
	})
	if err != nil {
		return err
	}
	return send(w, 200, object{"deviceId": id})
}
