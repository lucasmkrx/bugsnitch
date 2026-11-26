// SPDX-License-Identifier: MPL-2.0
package planning

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

// Project intake shares the revision and drift ledger used by published issues.
// The caller holds the project and any existing issue row locked across the GitHub read.
var ErrProjectBinding = errors.New("GitHub issue is already bound to another project item or repository")
var ErrProjectRevision = errors.New("GitHub issue needs a bounded, publishable title and canonical revision")

func RecordProjectIssue(ctx context.Context, tx *sql.Tx, ws, project string, install, repo int64, board int32, selection github.ProjectSelection) (bool, error) {
	i := selection.Issue
	revision := selectionRevision(i)
	if i.ID < 1 || i.Number < 1 || i.State != "open" || revision == "" || selection.ItemID == "" || selection.ProjectNode == "" {
		return false, ErrProjectRevision
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO planned_issue_state AS z
		(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,selection_revision,checked_at,next_check)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'open','current',$9,now(),now()+interval '5 minutes')
		ON CONFLICT (workspace_id,project_id,number) DO UPDATE SET
		  generation=z.generation+CASE WHEN z.digest<>'' AND (z.digest<>excluded.digest OR z.source_state<>'open') THEN 1 ELSE 0 END,
		  title=excluded.title,digest=excluded.digest,source_state='open',availability='current',selection_revision=excluded.selection_revision,
		  checked_at=now(),next_check=excluded.next_check,reason='',failures=0
		WHERE (z.github_id,z.installation_id,z.repository_id)=(excluded.github_id,excluded.installation_id,excluded.repository_id)`,
		ws, project, i.Number, i.ID, install, repo, i.Title, requirements(i), revision)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, ErrProjectBinding
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO github_project_tickets(workspace_id,project_id,number,project_node,item_node,board_number,github_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (workspace_id,project_id,number) DO NOTHING`, ws, project, i.Number, selection.ProjectNode, selection.ItemID, board, i.ID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT (project_node,item_node,board_number,github_id)=($4,$5,$6,$7)
		FROM github_project_tickets WHERE workspace_id=$1 AND project_id=$2 AND number=$3`, ws, project, i.Number, selection.ProjectNode, selection.ItemID, board, i.ID).Scan(&same)
	if err == nil && !same {
		err = ErrProjectBinding
	}
	return false, err
}
