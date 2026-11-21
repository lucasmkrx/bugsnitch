// SPDX-License-Identifier: MPL-2.0
package device

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func TestNextPrintsStartDocumentOnlyAfterCurrentCanonicalRead(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "fixture-read-only")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=test@example.invalid"}, args...)...)
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	if err = os.WriteFile(filepath.Join(dir, "example.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "example.txt")
	git("commit", "-m", "baseline")
	git("remote", "add", "origin", "https://github.com/fixture/next.git")
	p := profile{Credential: strings.Repeat("a", 64), Server: "http://localhost:8787", Enrollment: protocol.Enrollment{Workspace: "personal", DeviceID: "runner", Projects: []protocol.Project{{ID: "project", Repository: "fixture/next"}}}}
	if err = save(p, true); err != nil {
		t.Fatal(err)
	}
	if _, err = journal("INSERT INTO mappings VALUES('project'," + sqlValue(dir) + ");"); err != nil {
		t.Fatal(err)
	}
	if err = runner.AddProject("personal", "fixture/next", dir, runner.Config{Files: []string{"example.txt"}, Validate: []string{"/bin/cat", "example.txt"}}); err != nil {
		t.Fatal(err)
	}
	source := github.Issue{ID: 42, Number: 7, Title: "Correct display", Body: "Private evidence", UpdatedAt: "2026-11-21T10:00:00Z", State: "open"}
	selection := protocol.IssueSelection{Schema: 1, GitHubID: source.ID, RequirementsDigest: source.RequirementsDigest(), Number: 7, Repository: "fixture/next", URL: "https://github.com/fixture/next/issues/7", Title: source.Title, State: "open", UpdatedAt: source.UpdatedAt, Criteria: []string{"Show the correct label"}}
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	var calls []string
	http.DefaultTransport = requestRoute(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.Host+r.URL.Path)
		var response any
		switch r.URL.Host {
		case "localhost:8787":
			if r.Method != "POST" || r.URL.Path != "/api/v1/device/next" || r.Header.Get("Authorization") != "Bearer "+p.Credential {
				t.Fatal("unexpected service operation")
			}
			response = protocol.Recommendation{Selection: &selection}
		case "api.github.com":
			if r.Method != "GET" || r.URL.Path != "/repos/fixture/next/issues/7" || r.Header.Get("Authorization") != "Bearer fixture-read-only" {
				t.Fatal("unexpected provider operation")
			}
			response = map[string]any{"id": source.ID, "number": source.Number, "html_url": selection.URL, "state": source.State, "title": source.Title, "body": source.Body, "updated_at": source.UpdatedAt}
		default:
			t.Fatal("unapproved endpoint")
		}
		b, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(b))}, nil
	})
	var out bytes.Buffer
	if err = next([]string{"--project", "project"}, &out); err != nil {
		t.Fatal(err)
	}
	var actual runner.Issue
	if err = json.Unmarshal(out.Bytes(), &actual); err != nil || actual.GitHubID != 42 || actual.Number != 7 || actual.RequirementsDigest != source.RequirementsDigest() {
		t.Fatal(err, actual)
	}
	if strings.Contains(out.String(), source.Body) || strings.Contains(out.String(), dir) || strings.Contains(out.String(), "fixture-read-only") {
		t.Fatal("private context leaked")
	}
	if len(calls) != 2 {
		t.Fatal(calls)
	}
	out.Reset()
	source.Body = "Changed while timestamp stayed the same"
	if err = next([]string{"--project", "project"}, &out); err == nil || out.Len() > 0 {
		t.Fatal("stale body emitted a usable ticket", err)
	}
	source.Body = "Private evidence"
	source.ID = 99
	if err = next([]string{"--project", "project"}, &out); err == nil || out.Len() > 0 {
		t.Fatal("changed canonical identity was accepted", err)
	}
	attempts, err := runner.Summaries()
	if err != nil || len(attempts) != 0 {
		t.Fatal("selection dispatched execution", attempts, err)
	}
}
