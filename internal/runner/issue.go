// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func (i Issue) verify() error {
	if i.Schema != 1 || !repoPattern.MatchString(i.Repository) || i.Number < 1 || i.State != "open" || len(strings.TrimSpace(i.Title)) == 0 || len(i.Title) > 1024 || len(i.Criteria) == 0 || len(i.Criteria) > 32 {
		return errors.New("issue v1 requires an open issue, title and acceptance criteria")
	}
	if _, err := time.Parse(time.RFC3339, i.UpdatedAt); err != nil {
		return errors.New("invalid issue revision timestamp")
	}
	if i.URL != fmt.Sprintf("https://github.com/%s/issues/%d", i.Repository, i.Number) {
		return errors.New("issue link does not match selected repository and number")
	}
	for _, c := range i.Criteria {
		if strings.TrimSpace(c) == "" || len(c) > 2048 {
			return errors.New("invalid acceptance criterion")
		}
	}
	return nil
}

var verifyIssue = readGitHubIssue

func readGitHubIssue(ctx context.Context, i Issue) error {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", i.Repository, i.Number), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", github.APIVersion)
	if token := os.Getenv("BUGSNITCH_GITHUB_READ_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("GitHub issue verification unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub issue verification returned HTTP %d", resp.StatusCode)
	}
	var record struct {
		Number       int
		Title, State string
		UpdatedAt    string          `json:"updated_at"`
		URL          string          `json:"html_url"`
		PR           json.RawMessage `json:"pull_request"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&record); err != nil {
		return errors.New("invalid issue response")
	}
	if len(record.PR) > 0 || record.State != "open" || record.Number != i.Number || record.Title != i.Title || record.UpdatedAt != i.UpdatedAt || !strings.EqualFold(record.URL, i.URL) {
		return errors.New("GitHub issue changed or is ineligible; review its current revision")
	}
	return nil
}
