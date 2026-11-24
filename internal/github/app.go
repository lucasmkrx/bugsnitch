// SPDX-License-Identifier: MPL-2.0
// Package github adapts hosted domain operations to the private Rust GitHub client.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const APIVersion = "2026-03-10"

type App struct{ helper, keyFile, clientID, fixture string }

func NewApp(clientID, keyFile string) (*App, error) {
	return newApp(clientID, keyFile, "")
}

// NewLoopbackFixture runs the shipped provider client against an explicitly
// selected test listener. Production startup always uses NewApp.
func NewLoopbackFixture(clientID, keyFile, origin string) (*App, error) {
	if origin == "" {
		return nil, errors.New("fixture origin required")
	}
	return newApp(clientID, keyFile, origin)
}
func newApp(clientID, keyFile, fixture string) (*App, error) {
	if clientID == "" || keyFile == "" {
		return nil, errors.New("GitHub App client ID and key file required")
	}
	helper := os.Getenv("BUGSNITCH_PROVIDER")
	if helper == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		helper = filepath.Join(filepath.Dir(exe), "bugsnitch-provider")
	}
	if !filepath.IsAbs(helper) {
		return nil, errors.New("BUGSNITCH_PROVIDER must be an absolute path")
	}
	a := &App{helper: helper, clientID: clientID, keyFile: keyFile, fixture: fixture}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if a.command(ctx, "--check-key").Run() != nil {
		return nil, errors.New("install the matching bugsnitch-provider and check the GitHub App RSA key configuration")
	}
	return a, nil
}
func (a *App) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, a.helper, args...)
	cmd.Env = []string{"GITHUB_APP_CLIENT_ID=" + a.clientID, "GITHUB_APP_KEY_FILE=" + a.keyFile}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "SystemRoot"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	return cmd
}

type APIError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("GitHub returned HTTP %d", e.Status) }

type Issue struct {
	UpdatedAt   string `json:"updated_at"`
	ID          int64
	Number      int64
	Title       string
	Body        string
	State       string
	PullRequest json.RawMessage `json:"pull_request"`
	User        struct{ Login, Type string }
	App         *struct{ ID int64 } `json:"performed_via_github_app"`
}

func (a *App) Repository(ctx context.Context, installation, repository int64, name string) (*Repository, error) {
	return a.open(ctx, installation, repository, name, "issues")
}

// RepositoryID verifies the configured installation can see this exact repository name.
func (a *App) RepositoryID(ctx context.Context, installation int64, name string) (int64, error) {
	r, err := a.open(ctx, installation, 0, name, "discover")
	if err != nil {
		return 0, err
	}
	defer r.Close()
	var id int64
	err = r.call(ctx, fields{"op": "identity"}, &id)
	return id, err
}
func (r *Repository) Create(ctx context.Context, title, body string) (Issue, error) {
	var i Issue
	err := r.call(ctx, fields{"op": "create", "title": title, "body": body}, &i)
	return i, err
}
func (r *Repository) Get(ctx context.Context, n int64) (Issue, error) {
	var i Issue
	err := r.call(ctx, fields{"op": "get", "number": n}, &i)
	return i, err
}
func (r *Repository) Find(ctx context.Context, marker string, since time.Time) (*Issue, error) {
	var i *Issue
	err := r.call(ctx, fields{"op": "find", "marker": marker, "since": since.Add(-time.Minute).UTC().Format(time.RFC3339)}, &i)
	return i, err
}
func (a *App) ReadIssue(ctx context.Context, installation, repository int64, name string, n int64) (Issue, error) {
	r, err := a.open(ctx, installation, repository, name, "read_issue")
	if err != nil {
		return Issue{}, err
	}
	defer r.Close()
	return r.Get(ctx, n)
}
