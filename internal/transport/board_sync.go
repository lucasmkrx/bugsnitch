// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
)

type boardSyncState struct {
	Issue   int64  `json:"issueNumber"`
	Desired string `json:"desired"`
	Applied bool   `json:"applied"`
	Reason  string `json:"reason,omitempty"`
}

func listBoardSync(ctx context.Context, db *sql.DB, workspace, project string) ([]boardSyncState, error) {
	rows, err := db.QueryContext(ctx, `SELECT number,desired,applied IS NOT DISTINCT FROM desired,reason
	    FROM github_project_status WHERE workspace_id=$1 AND project_id=$2 ORDER BY next_run DESC,number LIMIT 200`, workspace, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []boardSyncState{}
	for rows.Next() {
		var item boardSyncState
		if err := rows.Scan(&item.Issue, &item.Desired, &item.Applied, &item.Reason); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
