// SPDX-License-Identifier: MPL-2.0
package storage

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// Preserve routing identities, occurrence metadata, receipts and idempotency keys when expiring content.
func Retain(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var held bool
	if err = tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock(628719845)").Scan(&held); err != nil || !held {
		return err
	}
	var due bool
	if err = tx.QueryRowContext(ctx, "SELECT NOT EXISTS(SELECT FROM retention_runs WHERE last_run>now()-interval '1 hour')").Scan(&due); err != nil || !due {
		return err
	}
	for _, q := range []string{
		"DELETE FROM sessions WHERE expires_at<=now()",
		"DELETE FROM pairings WHERE expires_at<=now()",
		"UPDATE devices SET previous_hash=NULL WHERE previous_hash IS NOT NULL AND rotated_at<now()-interval '1 day'",
		`DELETE FROM shared_diffs f USING projects p WHERE p.id=f.project_id AND f.created_at < now()-p.retention_days*interval '1 day'`,
		`UPDATE manager_messages m SET body='[Message expired]',redacted_at=now() FROM projects p WHERE p.id=m.project_id AND m.redacted_at IS NULL AND m.created_at<now()-p.retention_days*interval '1 day'`,
		`UPDATE incident_inbox b SET observed='[Observation expired]',expected='',steps='',frames='[]',redacted_at=now() FROM projects p WHERE p.id=b.project_id AND b.processed AND b.redacted_at IS NULL AND b.accepted_at<now()-p.retention_days*interval '1 day'`,
		`UPDATE incident_issues x SET status='blocked',reason='Observation expired before issue creation' FROM incidents i,projects p WHERE x.incident_id=i.id AND p.id=i.project_id AND x.status='queued' AND i.last_seen<now()-p.retention_days*interval '1 day'`,
		`UPDATE incidents i SET observed='[Observation expired]',expected='',steps='',redacted_at=now(),version=i.version+1 FROM projects p WHERE p.id=i.project_id AND i.redacted_at IS NULL AND i.last_seen<now()-p.retention_days*interval '1 day'`,
		`INSERT INTO retention_runs VALUES(true,now()) ON CONFLICT(id) DO UPDATE SET last_run=EXCLUDED.last_run`,
	} {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func Maintain(ctx context.Context, db *sql.DB) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, time.Minute)
		err := Retain(work, db)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("retention pass deferred")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
