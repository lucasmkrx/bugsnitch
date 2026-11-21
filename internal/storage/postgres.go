// SPDX-License-Identifier: MPL-2.0
package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(15)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err = Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(628719844); CREATE TABLE IF NOT EXISTS bugsnitch_migrations(name text PRIMARY KEY,digest text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		data, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(data))
		var saved string
		err = tx.QueryRowContext(ctx, "SELECT digest FROM bugsnitch_migrations WHERE name=$1", name).Scan(&saved)
		switch err {
		case nil:
			if saved != digest {
				return fmt.Errorf("migration checksum changed: %s", name)
			}
			continue
		case sql.ErrNoRows:
		default:
			return err
		}
		if _, err = tx.ExecContext(ctx, string(data)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO bugsnitch_migrations(name,digest) VALUES($1,$2)", name, digest); err != nil {
			return err
		}
	}
	return tx.Commit()
}
