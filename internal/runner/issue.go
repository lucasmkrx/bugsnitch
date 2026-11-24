// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

func (i Issue) verify() error {
	if i.Schema != 1 || !repoPattern.MatchString(i.Repository) || i.Number < 1 || i.State != "open" || len(strings.TrimSpace(i.Title)) == 0 || len(i.Title) > 1024 || len(i.Criteria) == 0 || len(i.Criteria) > 32 {
		return errors.New("issue v1 requires an open issue, title and acceptance criteria")
	}
	if _, err := time.Parse(time.RFC3339, i.UpdatedAt); err != nil {
		return errors.New("invalid issue revision timestamp")
	}
	if i.GitHubID != 0 || i.RequirementsDigest != "" {
		digest, err := hex.DecodeString(i.RequirementsDigest)
		if err != nil || len(digest) != 32 || i.GitHubID <= 0 {
			return errors.New("invalid canonical issue identity or requirements digest")
		}
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
	record, err := fetchGitHubIssue(ctx, i.Repository, i.Number)
	if err != nil {
		var status *github.APIError
		if errors.As(err, &status) {
			return fmt.Errorf("GitHub issue verification returned HTTP %d", status.Status)
		}
		return err
	}
	return record.matches(i)
}

type githubIssueRecord struct {
	github.Issue
	URL string `json:"html_url"`
}

func readIssueWithCLI(ctx context.Context, repository string, number int) (githubIssueRecord, error) {
	var record githubIssueRecord
	if !repoPattern.MatchString(repository) || number < 1 {
		return record, errors.New("invalid GitHub issue identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	route := fmt.Sprintf("repos/%s/issues/%d", repository, number)
	cmd := exec.CommandContext(ctx, "gh", "api", "--hostname", "github.com", "--method", "GET", route)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	output := &capped{max: 1 << 20}
	cmd.Stdout = output
	if cmd.Run() != nil || output.overflow || json.Unmarshal(output.buffer.Bytes(), &record) != nil {
		return record, errors.New("unable to read the selected GitHub issue with gh")
	}
	return record, nil
}

func fetchGitHubIssue(ctx context.Context, repository string, number int) (githubIssueRecord, error) {
	var record githubIssueRecord
	if os.Getenv("BUGSNITCH_GITHUB_AUTH") == "gh" && os.Getenv("BUGSNITCH_GITHUB_READ_TOKEN") == "" {
		return readIssueWithCLI(ctx, repository, number)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", repository, number), nil)
	if err != nil {
		return record, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", github.APIVersion)
	if token := os.Getenv("BUGSNITCH_GITHUB_READ_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return record, errors.New("GitHub issue verification unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return record, &github.APIError{Status: resp.StatusCode}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&record); err != nil {
		return record, errors.New("invalid issue response")
	}
	return record, nil
}

func (record githubIssueRecord) matches(i Issue) error {
	if len(record.PullRequest) > 0 || record.State != "open" || record.Number != int64(i.Number) || record.Title != i.Title || record.UpdatedAt != i.UpdatedAt || !strings.EqualFold(record.URL, i.URL) || (i.GitHubID > 0 && (record.ID != i.GitHubID || record.RequirementsDigest() != i.RequirementsDigest)) {
		return errors.New("GitHub issue changed or is ineligible; review its current revision")
	}
	return nil
}
