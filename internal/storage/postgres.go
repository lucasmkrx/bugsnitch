// SPDX-License-Identifier: MPL-2.0
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Open runs the matching native migrator before making a pool available to
// handlers or background workers. No provider credentials enter the child.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection settings")
	}
	path, err := helperPath()
	if err != nil {
		return nil, err
	}
	env := databaseEnvironment(dsn)
	if err = migrate(ctx, path, env); err != nil {
		return nil, err
	}
	connector := stdlib.GetConnector(*config)
	db := sql.OpenDB(&nativeConnector{Connector: connector, service: newCoordinator(connector.Driver(), path, env)})
	if err = db.PingContext(ctx); err == nil {
		err = Coordinate(ctx, db, "system.ready", nil, nil)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	db.SetMaxOpenConns(15)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

var ErrMissingMigrator = errors.New("install bugsnitch-server-migrate beside bugsnitch-server or set BUGSNITCH_MIGRATOR to its absolute path")

func helperPath() (string, error) {
	helper := os.Getenv("BUGSNITCH_MIGRATOR")
	if helper == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", ErrMissingMigrator
		}
		name := "bugsnitch-server-migrate"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		helper = filepath.Join(filepath.Dir(executable), name)
	}
	if !filepath.IsAbs(helper) {
		return "", ErrMissingMigrator
	}
	return helper, nil
}
func databaseEnvironment(dsn string) []string {
	env := []string{"DATABASE_URL=" + dsn}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PG") {
			env = append(env, entry)
			continue
		}
		switch key {
		case "HOME", "USER", "LOGNAME", "SystemRoot", "APPDATA", "KRB5CCNAME", "KRB5_CONFIG", "SSL_CERT_FILE", "SSL_CERT_DIR":
			env = append(env, entry)
		}
	}
	return env
}
func migrate(ctx context.Context, path string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	deadline, _ := ctx.Deadline()
	ms := max(1, time.Until(deadline).Milliseconds())
	cmd.Env = append(append([]string{}, env...), "BUGSNITCH_MIGRATION_TIMEOUT_MS="+strconv.FormatInt(ms, 10))
	// Child output may include libpq notices, so never forward it to hosted logs.
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return ErrMissingMigrator
		}
		return fmt.Errorf("PostgreSQL migration failed (exit %d); check connectivity, schema permissions and applied checksums", exit.ExitCode())
	}
	return nil
}
