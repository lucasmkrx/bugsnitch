// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"errors"
)

// The operator fixes the installation identity at service setup. A dashboard
// owner can connect repositories visible to that installation, never choose another one.
func (s *Server) connectGitHub(call *domainCall) error {
	var input struct{ Version int64 }
	if err := call.decode(&input); err != nil {
		return err
	}
	if input.Version < 1 {
		return bad("Provide the current project version")
	}
	if s.GitHubApp == nil || s.GitHubInstallation <= 0 {
		return problem{503, "Configure the workspace GitHub App and BUGSNITCH_GITHUB_INSTALLATION_ID first."}
	}
	ctx, ws, project := call.ctx, call.Workspace, call.Project
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var name string
		var version int64
		if err := tx.QueryRowContext(ctx, "SELECT repository,version FROM projects WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, project).Scan(&name, &version); err != nil {
			return err
		}
		if version != input.Version {
			return conflict("Project changed. Refresh before connecting.")
		}
		var installation, existing int64
		var active bool
		err := tx.QueryRowContext(ctx, "SELECT installation_id,repository_id,active FROM github_mappings WHERE workspace_id=$1 AND project_id=$2 FOR UPDATE", ws, project).Scan(&installation, &existing, &active)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if existing != 0 && installation != s.GitHubInstallation {
			return conflict("Project belongs to a different installation.")
		}
		id, err := s.GitHubApp.RepositoryID(ctx, s.GitHubInstallation, name)
		if err != nil || id < 1 {
			return problem{502, "GitHub could not verify this repository in the configured installation. Check its selected repositories."}
		}
		if existing != 0 && existing != id {
			return conflict("GitHub repository identity changed.")
		}
		if active {
			return nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES($1,$2,$3,$4)
            ON CONFLICT(workspace_id,project_id) DO UPDATE SET active=true`, ws, project, s.GitHubInstallation, id)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE projects SET version=version+1 WHERE workspace_id=$1 AND id=$2", ws, project); err != nil {
			return err
		}
		return event(ctx, tx, ws, project, "github.connected", project)
	})
	if err != nil {
		return err
	}
	return call.reply(200, object{"connected": true})
}
