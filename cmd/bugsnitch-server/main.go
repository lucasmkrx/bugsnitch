// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
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
func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	origin := env("BUGSNITCH_ORIGIN", "http://localhost:8787")
	owner := os.Getenv("BUGSNITCH_OWNER_TOKEN")
	dsn := os.Getenv("DATABASE_URL")
	if !transport.ValidOrigin(origin) || len(owner) < 32 || dsn == "" {
		return errors.New("set a valid BUGSNITCH_ORIGIN, DATABASE_URL and BUGSNITCH_OWNER_TOKEN (at least 32 characters)")
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	db, err := storage.Open(startup, dsn)
	cancel()
	if err != nil {
		return errors.New("could not connect or migrate PostgreSQL; check database configuration and migration checksums")
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
	service := transport.Server{GitHubApp: app, Plugin: transport.PluginAuth{LocalToken: os.Getenv("BUGSNITCH_MCP_TOKEN"), Issuer: os.Getenv("MCP_OAUTH_ISSUER"), Introspection: os.Getenv("MCP_OAUTH_INTROSPECTION"), ClientID: os.Getenv("MCP_OAUTH_CLIENT_ID"), ClientSecret: os.Getenv("MCP_OAUTH_CLIENT_SECRET"), OwnerSubject: os.Getenv("MCP_OAUTH_OWNER_SUBJECT")}, DB: db, OwnerToken: owner, Origin: origin, WebDir: env("BUGSNITCH_WEB_DIR", "web/dist"), Incidents: store, GitHubSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"), SentrySecret: os.Getenv("SENTRY_CLIENT_SECRET")}
	server := http.Server{Addr: env("BUGSNITCH_LISTEN", "127.0.0.1:8787"), Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10}
	go store.Run(ctx, app)
	go (planning.Store{DB: db}).Run(ctx, app)
	go func() {
		<-ctx.Done()
		end, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(end)
	}()
	slog.Info("listening", "address", server.Addr, "protocol", 1)
	if err = server.ListenAndServe(); errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
