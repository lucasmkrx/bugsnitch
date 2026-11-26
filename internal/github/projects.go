// SPDX-License-Identifier: MPL-2.0
package github

import "context"

type ProjectTicket struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
	ItemID string `json:"itemId"`
}

type ProjectPage struct {
	Number     int32           `json:"number"`
	Title      string          `json:"title"`
	Issues     []ProjectTicket `json:"issues"`
	NextCursor *string         `json:"nextCursor"`
}

type ProjectSelection struct {
	ProjectNode string `json:"projectNode"`
	ItemID      string `json:"itemId"`
	Status      string `json:"status"`
	Issue       Issue  `json:"issue"`
}

func (a *App) ListProjectIssues(ctx context.Context, install, repo int64, name string, board int32, after *string) (ProjectPage, error) {
	var page ProjectPage
	r, err := a.open(ctx, install, repo, name, "project_read")
	if err != nil {
		return page, err
	}
	defer r.Close()
	err = r.call(ctx, fields{"op": "project_issues", "board": board, "after": after}, &page)
	return page, err
}

func (a *App) ReadProjectIssue(ctx context.Context, install, repo int64, name string, board, issue int32, item string) (ProjectSelection, error) {
	var selection ProjectSelection
	r, err := a.open(ctx, install, repo, name, "project_read")
	if err != nil {
		return selection, err
	}
	defer r.Close()
	err = r.call(ctx, fields{"op": "project_import", "board": board, "issue": issue, "item": item}, &selection)
	return selection, err
}

// SetProjectStatus re-reads the bound board item and issue before updating the Status field.
func (a *App) SetProjectStatus(ctx context.Context, installation, repository int64, name string, issue, githubID int64, projectNode, itemNode, status string) error {
	r, err := a.open(ctx, installation, repository, name, "project_write")
	if err != nil {
		return err
	}
	defer r.Close()
	return r.call(ctx, fields{"op": "project_status", "issue": issue, "github_id": githubID, "project_node": projectNode, "item_node": itemNode, "status": status}, nil)
}
