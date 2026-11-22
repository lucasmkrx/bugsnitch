// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
)

func (s *Server) retention(call *domainCall) error {
	ws := call.Workspace
	var b struct {
		Days    int
		Version int64
	}
	if err := call.decode(&b); err != nil {
		return err
	}
	if b.Days < 1 || b.Days > 3650 {
		return bad("Choose a retention period from 1 to 3650 days")
	}
	ctx := call.ctx
	p := call.ID
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
	return call.reply(200, object{"days": b.Days})
}
