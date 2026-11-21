// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

type Server struct {
	GitHubApp                  *github.App
	Plugin                     PluginAuth
	DB                         *sql.DB
	OwnerToken, Origin, WebDir string
	Incidents                  incidents.Store
	SentrySecret, GitHubSecret string
	mu                         sync.Mutex
	buckets                    map[string]bucket
}
type bucket struct {
	start time.Time
	n     int
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
type endpoint func(http.ResponseWriter, *http.Request) error
type ownerEndpoint func(http.ResponseWriter, *http.Request, string) error

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func send(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		return problem{415, "Use application/json"}
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return bad("Invalid JSON request")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return bad("Expected a single JSON document")
	}
	return nil
}
func respondError(w http.ResponseWriter, err error) {
	p := problem{500, "Request failed"}
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &p):
	case errors.Is(err, sql.ErrNoRows):
		p = problem{404, "Record unavailable"}
	case errors.As(err, &pg) && pg.Code == "23505":
		p = problem{409, "Record already exists"}
	case errors.As(err, &pg) && pg.Code == "23503":
		p = problem{404, "Related record unavailable"}
	}
	_ = send(w, p.code, object{"error": p.message})
}
func wrap(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			respondError(w, err)
		}
	}
}
func (s *Server) owner(fn ownerEndpoint) http.HandlerFunc {
	return wrap(func(w http.ResponseWriter, r *http.Request) error {
		c, err := r.Cookie("bugsnitch_session")
		if err != nil {
			return problem{401, "Sign in to Bugsnitch"}
		}
		var ws string
		err = s.DB.QueryRowContext(r.Context(), "SELECT workspace_id FROM sessions WHERE token_hash=$1 AND expires_at>now()", hash(c.Value)).Scan(&ws)
		if err == sql.ErrNoRows {
			return problem{401, "Session expired"}
		}
		if err != nil {
			return err
		}
		return fn(w, r, ws)
	})
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
	_, err := tx.ExecContext(ctx, "INSERT INTO events(workspace_id,project_id,kind,subject_id) VALUES($1,NULLIF($2,''),$3,$4)", ws, p, kind, id)
	return err
}
func (s *Server) allow(r *http.Request) bool {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets == nil {
		s.buckets = make(map[string]bucket)
	}
	now := time.Now()
	b := s.buckets[ip]
	if now.Sub(b.start) > time.Minute {
		b = bucket{start: now}
	}
	if b.n >= 30 {
		return false
	}
	b.n++
	if len(s.buckets) >= 4096 {
		for k, v := range s.buckets {
			if now.Sub(v.start) > time.Minute {
				delete(s.buckets, k)
			}
		}
		if len(s.buckets) >= 4096 && s.buckets[ip].start.IsZero() {
			return false
		}
	}
	s.buckets[ip] = b
	return true
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.mountMCP(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _ = send(w, 200, object{"status": "alive"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if s.DB.PingContext(ctx) != nil {
			_ = send(w, 503, object{"status": "unavailable"})
			return
		}
		_ = send(w, 200, object{"status": "ready", "protocol": 1})
	})
	mux.HandleFunc("POST /api/v1/login", wrap(s.login))
	mux.HandleFunc("POST /api/v1/logout", s.owner(s.logout))
	mux.HandleFunc("GET /api/v1/state", s.owner(s.state))
	mux.HandleFunc("POST /api/v1/projects/{project}/pulls/{number}/refresh", s.owner(s.refreshPull))
	mux.HandleFunc("POST /api/v1/projects/{project}/pulls/{number}/evidence", s.owner(s.recordPull))
	mux.HandleFunc("POST /api/v1/publications", s.owner(s.publication))
	mux.HandleFunc("POST /api/v1/publications/{id}/approve", s.owner(s.publish))
	mux.HandleFunc("POST /api/v1/plans", s.owner(s.proposePlan))
	mux.HandleFunc("POST /api/v1/plans/{id}/approve", s.owner(s.approvePlan))
	mux.HandleFunc("POST /api/v1/plans/{id}/evidence", s.owner(s.attestPrerequisite))
	mux.HandleFunc("POST /api/v1/projects", s.owner(s.createProject))
	mux.HandleFunc("POST /api/v1/projects/{id}/policy", s.owner(s.policy))
	mux.HandleFunc("POST /api/v1/limits", s.owner(s.limits))
	mux.HandleFunc("POST /api/v1/projects/{id}/dispatch", s.owner(s.dispatch))
	mux.HandleFunc("POST /api/v1/decisions/{id}/answer", s.owner(s.answer))
	mux.HandleFunc("POST /api/v1/pairings", s.owner(s.pairing))
	mux.HandleFunc("POST /api/v1/devices/{id}/revoke", s.owner(s.revoke))
	mux.HandleFunc("POST /api/v1/attempts/{id}/control", s.owner(s.control))
	mux.HandleFunc("POST /api/v1/messages", s.owner(s.message))
	mux.HandleFunc("POST /api/v1/incidents", s.owner(s.report))
	mux.HandleFunc("POST /api/v1/projects/{project}/incidents/{id}/promote", s.owner(s.promote))
	for _, method := range []string{"GET", "POST"} {
		mux.HandleFunc(method+" /api/v1/projects/{project}/incidents/{id}/verification", s.owner(func(w http.ResponseWriter, r *http.Request, ws string) error {
			s.Incidents.VerificationHTTP(w, r, ws, r.PathValue("project"), r.PathValue("id"))
			return nil
		}))
	}
	mux.HandleFunc("POST /api/v1/device/pair", wrap(s.redeem))
	mux.HandleFunc("POST /api/v1/device/admission", wrap(s.admission))
	mux.HandleFunc("POST /api/v1/device/sync", wrap(s.syncDevice))
	mux.Handle("POST /hooks/sentry", s.Incidents.SentryWebhook(s.SentrySecret))
	mux.Handle("POST /hooks/github", s.Incidents.GitHubWebhook(s.GitHubSecret))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { respondError(w, problem{404, "Unknown API operation"}) })
	mux.HandleFunc("/", s.frontend)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := random(12)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.Origin {
			respondError(w, denied)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			respondError(w, denied)
			return
		}
		start := time.Now()
		mux.ServeHTTP(w, r)
		slog.Debug("request", "id", id, "method", r.Method, "route", r.Pattern, "duration_ms", time.Since(start).Milliseconds())
	})
}
func (s *Server) frontend(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if !filepath.IsLocal(name) && name != "" {
		http.NotFound(w, r)
		return
	}
	if name == "" {
		name = "index.html"
	}
	path := filepath.Join(s.WebDir, name)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		if filepath.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		path = filepath.Join(s.WebDir, "index.html")
	}
	http.ServeFile(w, r, path)
}
func (s *Server) cookie(w http.ResponseWriter, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "bugsnitch_session", Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Origin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	if !s.allow(r) {
		return problem{429, "Try again in one minute"}
	}
	var body struct{ Token string }
	if err := decode(w, r, &body); err != nil {
		return err
	}
	if len(s.OwnerToken) < 32 || subtle.ConstantTimeCompare([]byte(hash(s.OwnerToken)), []byte(hash(body.Token))) != 1 {
		return problem{401, "Invalid owner credential"}
	}
	token := random(32)
	_, err := s.DB.ExecContext(r.Context(), "INSERT INTO sessions(token_hash,workspace_id,expires_at) VALUES($1,'personal',now()+interval '12 hours')", hash(token))
	if err != nil {
		return err
	}
	s.cookie(w, token, 43200)
	return send(w, 200, object{"authenticated": true})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request, ws string) error {
	var empty struct{}
	if err := decode(w, r, &empty); err != nil {
		return err
	}
	c, _ := r.Cookie("bugsnitch_session")
	_, err := s.DB.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash=$1 AND workspace_id=$2", hash(c.Value), ws)
	if err != nil {
		return err
	}
	s.cookie(w, "", -1)
	return send(w, 200, object{"authenticated": false})
}
func ValidOrigin(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == "" && (u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))
}

var repoPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,38}/[a-zA-Z0-9_.-]{1,100}$`)
var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,100}$`)
