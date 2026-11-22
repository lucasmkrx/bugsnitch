// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"regexp"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

// Server implements application operations on the private domain socket.
type Server struct {
	DB        *sql.DB
	Origin    string
	GitHubApp *github.App
	Incidents incidents.Store
}
type problem struct {
	code    int
	message string
}

func (p problem) Error() string     { return p.message }
func bad(message string) error      { return problem{400, message} }
func conflict(message string) error { return problem{409, message} }

var denied = problem{403, "Permission denied"}

type object = map[string]any

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func transaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func event(ctx context.Context, tx *sql.Tx, ws, p, kind, id string) error {
	return audit.Record(ctx, tx, ws, p, kind, id)
}

var repoPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,38}/[a-zA-Z0-9_.-]{1,100}$`)
var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,100}$`)
var shaPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
