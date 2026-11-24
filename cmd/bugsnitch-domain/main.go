// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/linear"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
	"github.com/lucasmkrx/bugsnitch/internal/transport"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("startup or shutdown failed", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) != 2 || !filepath.IsAbs(os.Args[1]) {
		return errors.New("this private worker requires its server-owned socket path")
	}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); stop() }()
	dsn := os.Getenv("DATABASE_URL")
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	db, err := storage.Open(startup, dsn)
	cancel()
	if err != nil {
		if errors.Is(err, storage.ErrMissingMigrator) {
			return err
		}
		return errors.New("could not connect or migrate PostgreSQL; check database configuration, schema permissions and migration checksums")
	}
	defer db.Close()
	var app *github.App
	clientID, key := os.Getenv("GITHUB_APP_CLIENT_ID"), os.Getenv("GITHUB_APP_KEY_FILE")
	if clientID != "" || key != "" {
		app, err = github.NewApp(clientID, key)
		if err != nil {
			return errors.New("invalid GitHub App configuration")
		}
	}
	store := incidents.Store{DB: db}
	service := transport.Server{GitHubApp: app, DB: db, Incidents: store, Origin: os.Getenv("BUGSNITCH_ORIGIN"), Linear: linear.New(os.Getenv("BUGSNITCH_LINEAR_API_KEY"))}
	if raw := os.Getenv("BUGSNITCH_GITHUB_INSTALLATION_ID"); raw != "" {
		service.GitHubInstallation, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || service.GitHubInstallation <= 0 {
			return errors.New("set a positive BUGSNITCH_GITHUB_INSTALLATION_ID")
		}
	}
	listener, err := net.Listen("unix", os.Args[1])
	if err != nil {
		return errors.New("private domain socket unavailable")
	}
	defer listener.Close()
	if err = os.Chmod(os.Args[1], 0600); err != nil {
		return errors.New("cannot restrict domain socket")
	}
	var work sync.WaitGroup
	defer func() { stop(); work.Wait() }()
	if token := os.Getenv("BUGSNITCH_LINEAR_AGENT_TOKEN"); token != "" {
		service.LinearAgent = linear.New("Bearer " + token)
		work.Go(func() { service.ProcessLinearSessions(ctx) })
	}
	work.Go(func() { storage.Maintain(ctx, db) })
	work.Go(func() { runBackground(ctx, db, dsn, app) })
	go func() { <-ctx.Done(); _ = listener.Close() }()
	if _, err = os.Stdout.Write([]byte{1}); err != nil {
		return err
	}
	// Bound active domain operations, including callers that drop their IPC
	// connection while an authorized transaction or provider call is pending.
	permits := make(chan struct{}, 32)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("private domain accept failed")
		}
		select {
		case permits <- struct{}{}:
		case <-ctx.Done():
			conn.Close()
			return nil
		}
		work.Go(func() { defer func() { <-permits }(); defer conn.Close(); serveCommand(ctx, &service, conn) })
	}
}

func serveCommand(ctx context.Context, s *transport.Server, conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	var length uint32
	if binary.Read(conn, binary.BigEndian, &length) != nil || length == 0 || length > 256<<10 {
		return
	}
	b := make([]byte, length)
	if _, err := io.ReadFull(conn, b); err != nil {
		return
	}
	var req transport.DomainRequest
	if json.Unmarshal(b, &req) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 28*time.Second)
	defer cancel()
	// A disconnected frontend cancels in-flight work and retires its scarce
	// database/provider resources. Operations are never replayed by the IPC layer.
	go func() { var one [1]byte; _, _ = conn.Read(one[:]); cancel() }()
	reply, err := json.Marshal(s.Domain(ctx, req))
	if err != nil || len(reply) > 32<<20 {
		return
	}
	if binary.Write(conn, binary.BigEndian, uint32(len(reply))) == nil {
		_, _ = conn.Write(reply)
	}
}
