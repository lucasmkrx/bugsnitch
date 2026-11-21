// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

type offlineGitHub func(*http.Request) (*http.Response, error)

func (f offlineGitHub) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestReadCurrentGithubIssueRejectsDrift(t *testing.T) {
	i := Issue{Schema: 1, Repository: "owner/repo", Number: 12, URL: "https://github.com/owner/repo/issues/12", Title: "Observed display bug", State: "open", UpdatedAt: "2026-11-21T10:00:00Z", Criteria: []string{"Match display spec"}}
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "sensitive-marker")
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	for _, mode := range []string{"current", "closed", "edited", "transferred", "pull", "rate_limited", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			http.DefaultTransport = offlineGitHub(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://api.github.com/repos/owner/repo/issues/12" || r.Header.Get("X-GitHub-Api-Version") != github.APIVersion || r.Header.Get("Authorization") != "Bearer sensitive-marker" {
					t.Fatal("wrong repository or API contract")
				}
				obj := map[string]any{"number": 12, "state": "open", "html_url": i.URL, "title": i.Title, "updated_at": i.UpdatedAt, "body": "sensitive-marker"}
				code := 200
				headers := http.Header{}
				switch mode {
				case "closed":
					obj["state"] = "closed"
				case "edited":
					obj["updated_at"] = "2026-11-21T10:01:00Z"
				case "transferred":
					obj["html_url"] = "https://github.com/other/repo/issues/12"
				case "pull":
					obj["pull_request"] = map[string]any{}
				case "rate_limited":
					code = 429
				case "redirect":
					code = 301
					headers.Set("Location", "https://example.invalid/")
				}
				data, _ := json.Marshal(obj)
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(data))), Header: headers, Request: r}, nil
			})
			err := readGitHubIssue(context.Background(), i)
			if (err == nil) != (mode == "current") || calls != 1 || strings.Contains(fmt.Sprint(err), "sensitive-marker") {
				t.Fatal("issue revision or privacy invariant violated")
			}
		})
	}
}
func TestStageOverridesInherit(t *testing.T) {
	c := Config{Model: "default-model", Reasoning: "default-effort", Stages: map[string]StageSettings{"review": {Model: "review-model", TimeoutSeconds: 80}}}
	if got := c.forStage("review"); got.Model != "review-model" || got.Reasoning != "default-effort" || got.TimeoutSeconds != 80 {
		t.Fatal(got)
	}
	if got := c.forStage("implement"); got.Model != "default-model" || got.TimeoutSeconds != 240 {
		t.Fatal(got)
	}
}
