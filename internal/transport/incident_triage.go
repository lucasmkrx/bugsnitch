// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

type incidentRepository struct {
	name, observed, expected, steps string
	installation, repository        int64
}

func (s *Server) incidentRepository(c *domainCall) (incidentRepository, error) {
	var repo incidentRepository
	err := s.DB.QueryRowContext(c.ctx, `SELECT p.repository,COALESCE(m.installation_id,0),COALESCE(m.repository_id,0),i.observed,i.expected,i.steps
		FROM incidents i JOIN projects p ON (p.workspace_id,p.id)=(i.workspace_id,i.project_id)
		LEFT JOIN github_mappings m ON (m.workspace_id,m.project_id)=(i.workspace_id,i.project_id) AND m.active
		WHERE i.workspace_id=$1 AND i.project_id=$2 AND i.id=$3`, c.Workspace, c.Project, c.ID).
		Scan(&repo.name, &repo.installation, &repo.repository, &repo.observed, &repo.expected, &repo.steps)
	if err == nil && (s.GitHubApp == nil || repo.repository == 0) {
		err = problem{503, "Connect the project repository to the GitHub App to find related work."}
	}
	return repo, err
}

type workSuggestion struct {
	Number int64    `json:"number"`
	Title  string   `json:"title"`
	Pull   bool     `json:"pull"`
	Match  []string `json:"match"`
	score  int
}

func triageWords(input string) []string {
	var separated strings.Builder
	var previous rune
	for _, current := range input {
		if unicode.IsLower(previous) && unicode.IsUpper(current) {
			separated.WriteByte(' ')
		}
		separated.WriteRune(unicode.ToLower(current))
		previous = current
	}
	words := strings.FieldsFunc(separated.String(), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	var result []string
	for _, word := range words {
		if len(word) < 3 || len(word) > 48 || slices.Contains(result, word) {
			continue
		}
		switch word {
		case "the", "and", "for", "this", "that", "with", "when", "from", "was", "are", "has", "have", "will", "user", "users", "app", "issue", "error", "exception", "failed", "fails", "expected", "observed", "reported", "behavior", "application", "bug", "fix", "reproduce", "steps", "sentry", "redacted":
			continue
		}
		result = append(result, word)
		if len(result) == 96 {
			break
		}
	}
	return result
}

func rankRelatedWork(report string, items []github.RelatedWork) []workSuggestion {
	terms := triageWords(report)
	ranked := make([]workSuggestion, 0)
	for _, item := range items {
		title, body := triageWords(item.Title), triageWords(item.Body)
		r := workSuggestion{Number: item.Number, Title: item.Title, Pull: item.Pull}
		for _, word := range terms {
			switch {
			case slices.Contains(title, word):
				r.score += 4
			case slices.Contains(body, word):
				r.score++
			default:
				continue
			}
			r.Match = append(r.Match, word)
		}
		if len(r.Match) >= 2 {
			r.Match = r.Match[:min(len(r.Match), 5)]
			ranked = append(ranked, r)
		}
	}
	slices.SortStableFunc(ranked, func(a, b workSuggestion) int { return b.score - a.score })
	return ranked[:min(len(ranked), 8)]
}

func (s *Server) relatedIncidentWork(c *domainCall) error {
	repo, err := s.incidentRepository(c)
	if err != nil {
		return err
	}
	items, err := s.GitHubApp.RecentWork(c.ctx, repo.installation, repo.repository, repo.name)
	if err != nil {
		return problem{502, "GitHub could not list recent work. Check the repository connection and Issues permission."}
	}
	return c.reply(200, object{"items": rankRelatedWork(strings.Join([]string{repo.observed, repo.expected, repo.steps}, " "), items)})
}

func (s *Server) linkIncidentIssue(c *domainCall) error {
	var in struct {
		Number  int64 `json:"number"`
		Version int64 `json:"version"`
	}
	if err := c.decode(&in); err != nil {
		return err
	}
	if in.Number < 1 || in.Number > 2147483647 || in.Version < 1 {
		return bad("Select an open GitHub issue and the current incident version.")
	}
	repo, err := s.incidentRepository(c)
	if err != nil {
		return err
	}
	issue, err := s.GitHubApp.ReadIssue(c.ctx, repo.installation, repo.repository, repo.name, in.Number)
	var api *github.APIError
	if errors.Is(err, github.ErrSubjectChanged) || errors.As(err, &api) && (api.Status == 404 || api.Status == 410) || err == nil && issue.State != "open" {
		return conflict("Select an open issue in the connected repository.")
	}
	if err != nil {
		return problem{502, "GitHub could not verify the issue. Check the repository connection."}
	}
	err = s.Incidents.LinkIssue(c.ctx, c.Workspace, c.Project, c.ID, in.Version, repo.installation, repo.repository, repo.name, issue)
	if errors.Is(err, incidents.ErrConflict) {
		return conflict("The incident changed or the issue is already linked. Refresh the incident before choosing an issue.")
	}
	if err != nil {
		return incidentError(err)
	}
	return c.reply(200, object{"issueNumber": issue.Number, "status": "linked"})
}
