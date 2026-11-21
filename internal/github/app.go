// Package github confines installation credentials and issue writes to the service.
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const APIVersion = "2026-03-10"

var repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)

type App struct {
	clientID string
	key      *rsa.PrivateKey
	client   *http.Client
	base     string
}

func NewApp(clientID, keyFile string) (*App, error) {
	if clientID == "" || keyFile == "" {
		return nil, errors.New("GitHub App client ID and key file required")
	}
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, errors.New("cannot read GitHub App key file")
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid GitHub App key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		value, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			return nil, errors.New("GitHub App requires RSA PEM key")
		}
		var ok bool
		key, ok = value.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("GitHub App requires an RSA signing key")
		}
	}
	return &App{clientID: clientID, key: key, base: "https://api.github.com", client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (a *App) jwt() (string, error) {
	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(8 * time.Minute).Unix(), "iss": a.clientID})
	if err != nil {
		return "", err
	}
	content := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(content))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return content + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

type APIError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("GitHub returned HTTP %d", e.Status) }
func delay(h http.Header) time.Duration {
	if n, e := strconv.Atoi(h.Get("Retry-After")); e == nil && n > 0 && n <= 86400 {
		return time.Duration(n) * time.Second
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if n, e := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); e == nil {
			d := time.Until(time.Unix(n, 0))
			if d > 0 && d <= 24*time.Hour {
				return d + time.Second
			}
		}
	}
	return 0
}
func (a *App) request(ctx context.Context, token, method, route string, body, out any, code int) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+route, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	req.Header.Set("User-Agent", "Bugsnitch/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return errors.New("GitHub connection unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != code {
		return &APIError{resp.StatusCode, delay(resp.Header)}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return errors.New("unexpected GitHub response")
	}
	return nil
}

type Repository struct {
	app              *App
	token, path, bot string
	appID            int64
}

func (a *App) Repository(ctx context.Context, installation, repository int64, name string) (*Repository, error) {
	return a.repository(ctx, installation, repository, name, map[string]string{"issues": "write"})
}
func (a *App) repository(ctx context.Context, installation, repository int64, name string, permissions map[string]string) (*Repository, error) {
	if installation < 1 || repository < 1 || !repoName.MatchString(name) || strings.HasSuffix(name, "/.") || strings.HasSuffix(name, "/..") {
		return nil, errors.New("invalid repository mapping")
	}
	jwt, err := a.jwt()
	if err != nil {
		return nil, err
	}
	var ident struct {
		ID   int64
		Slug string
	}
	if err = a.request(ctx, jwt, "GET", "/app", nil, &ident, 200); err != nil {
		return nil, err
	}
	if ident.ID < 1 || ident.Slug == "" {
		return nil, errors.New("unknown GitHub App identity")
	}
	body := map[string]any{"repository_ids": []int64{repository}, "permissions": permissions}
	var auth struct{ Token string }
	if err = a.request(ctx, jwt, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", installation), body, &auth, 201); err != nil {
		return nil, err
	}
	if auth.Token == "" {
		return nil, errors.New("GitHub installation token unavailable")
	}
	var repo struct {
		ID   int64
		Full string `json:"full_name"`
	}
	if err = a.request(ctx, auth.Token, "GET", fmt.Sprintf("/repositories/%d", repository), nil, &repo, 200); err != nil {
		return nil, err
	}
	if repo.ID != repository || !strings.EqualFold(name, repo.Full) {
		return nil, errors.New("GitHub repository identity changed")
	}
	return &Repository{app: a, token: auth.Token, path: "/repos/" + name, appID: ident.ID, bot: ident.Slug + "[bot]"}, nil
}

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

func (r *Repository) Create(ctx context.Context, title, body string) (Issue, error) {
	var i Issue
	err := r.app.request(ctx, r.token, "POST", r.path+"/issues", map[string]string{"title": title, "body": body}, &i, 201)
	if err == nil && (i.ID < 1 || i.Number < 1) {
		return Issue{}, errors.New("GitHub issue response lacks identity")
	}
	return i, err
}
func (r *Repository) Get(ctx context.Context, n int64) (Issue, error) {
	var i Issue
	err := r.app.request(ctx, r.token, "GET", fmt.Sprintf("%s/issues/%d", r.path, n), nil, &i, 200)
	if err == nil && (i.ID < 1 || i.Number != n || len(i.PullRequest) > 0) {
		return Issue{}, ErrSubjectChanged
	}
	return i, err
}

// Find authenticates authorship as well as checking the private outbox marker.
// The caller must keep an uncertain write pending if no matching issue is visible.
func (r *Repository) Find(ctx context.Context, marker string, since time.Time) (*Issue, error) {
	var found *Issue
	for page := 1; page <= 10; page++ {
		query := url.Values{"state": {"all"}, "per_page": {"100"}, "sort": {"created"}, "direction": {"desc"}, "page": {strconv.Itoa(page)}, "since": {since.Add(-time.Minute).UTC().Format(time.RFC3339)}}
		var issues []Issue
		if err := r.app.request(ctx, r.token, "GET", r.path+"/issues?"+query.Encode(), nil, &issues, 200); err != nil {
			return nil, err
		}
		for _, i := range issues {
			if i.ID <= 0 || i.Number <= 0 || len(i.PullRequest) > 0 || !strings.HasSuffix(i.Body, "\n"+marker) {
				continue
			}
			if i.App != nil {
				if i.App.ID != r.appID {
					continue
				}
			} else if i.User.Type != "Bot" || !strings.EqualFold(i.User.Login, r.bot) {
				continue
			}
			if found != nil && found.ID != i.ID {
				return nil, errors.New("conflicting GitHub issue markers")
			}
			copy := i
			found = &copy
		}
		if len(issues) < 100 {
			return found, nil
		}
	}
	return nil, errors.New("GitHub reconciliation exceeded page limit")
}

// ReadIssue is used for drift reconciliation with a single-repository read-only token.
func (a *App) ReadIssue(ctx context.Context, installation, repository int64, name string, n int64) (Issue, error) {
	r, err := a.repository(ctx, installation, repository, name, map[string]string{"issues": "read"})
	if err != nil {
		return Issue{}, err
	}
	return r.Get(ctx, n)
}
