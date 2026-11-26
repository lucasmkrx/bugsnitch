// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
)

const projectMapping = `SELECT p.repository,m.installation_id,m.repository_id FROM projects p
 JOIN github_mappings m ON (m.workspace_id,m.project_id)=(p.workspace_id,p.id)
 WHERE p.workspace_id=$1 AND p.id=$2 AND m.active`

func validBoardToken(s string, max int) bool {
	if len(s) < 1 || len(s) > max {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func projectProviderError(err error) error {
	if errors.Is(err, github.ErrSubjectChanged) {
		return conflict("GitHub Project or issue changed. Refresh the board and select an open issue.")
	}
	return problem{502, "GitHub could not verify the project. Check the organization project number and GitHub App Projects permission."}
}

func (s *Server) projectIssues(c *domainCall) error {
	var input struct {
		Number int32
		After  *string
	}
	if err := c.decode(&input); err != nil {
		return err
	}
	if input.Number < 1 || input.After != nil && !validBoardToken(*input.After, 1024) {
		return bad("Provide a GitHub project number and a valid page cursor")
	}
	if s.GitHubApp == nil {
		return problem{503, "Configure the workspace GitHub App first."}
	}
	var name string
	var install, repo int64
	if err := s.DB.QueryRowContext(c.ctx, projectMapping, c.Workspace, c.Project).Scan(&name, &install, &repo); err != nil {
		return err
	}
	page, err := s.GitHubApp.ListProjectIssues(c.ctx, install, repo, name, input.Number, input.After)
	if err != nil {
		return projectProviderError(err)
	}
	page.Title = incidents.Redact(page.Title)
	for i := range page.Issues {
		page.Issues[i].Title = incidents.Redact(page.Issues[i].Title)
		page.Issues[i].Status = incidents.Redact(page.Issues[i].Status)
	}
	return c.reply(200, page)
}

func (s *Server) importProjectIssue(c *domainCall) error {
	var input struct {
		Number int32
		Issue  int32
		ItemID string `json:"itemId"`
	}
	if err := c.decode(&input); err != nil {
		return err
	}
	if input.Number < 1 || input.Issue < 1 || !validBoardToken(input.ItemID, 256) {
		return bad("Select an issue from the GitHub project")
	}
	if s.GitHubApp == nil {
		return problem{503, "Configure the workspace GitHub App first."}
	}
	var selection github.ProjectSelection
	err := transaction(c.ctx, s.DB, func(tx *sql.Tx) error {
		var name string
		var install, repo int64
		if err := tx.QueryRowContext(c.ctx, projectMapping+" FOR UPDATE OF p FOR SHARE OF m", c.Workspace, c.Project).Scan(&name, &install, &repo); err != nil {
			return err
		}
		// Serialize with the drift worker before obtaining an authoritative issue snapshot.
		var existing int64
		err := tx.QueryRowContext(c.ctx, "SELECT number FROM planned_issue_state WHERE workspace_id=$1 AND project_id=$2 AND number=$3 FOR UPDATE", c.Workspace, c.Project, input.Issue).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		selection, err = s.GitHubApp.ReadProjectIssue(c.ctx, install, repo, name, input.Number, input.Issue, input.ItemID)
		if err != nil {
			return projectProviderError(err)
		}
		first, err := planning.RecordProjectIssue(c.ctx, tx, c.Workspace, c.Project, install, repo, input.Number, selection)
		if err != nil {
			if errors.Is(err, planning.ErrProjectBinding) || errors.Is(err, planning.ErrProjectRevision) {
				return conflict(err.Error())
			}
			return err
		}
		if first {
			return event(c.ctx, tx, c.Workspace, c.Project, "github_project.imported", strconv.FormatInt(selection.Issue.Number, 10))
		}
		return nil
	})
	if err != nil {
		return err
	}
	i := selection.Issue
	return c.reply(200, object{"number": i.Number, "title": i.Title, "updatedAt": i.UpdatedAt, "status": incidents.Redact(selection.Status), "boardNumber": input.Number})
}
