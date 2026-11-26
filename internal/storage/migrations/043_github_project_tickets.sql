-- The board and item identities come from GitHub, scoped to the connected repository.
CREATE TABLE github_project_tickets (
 workspace_id text NOT NULL, project_id text NOT NULL, number bigint NOT NULL,
 project_node text NOT NULL, item_node text NOT NULL, board_number bigint NOT NULL CHECK(board_number>0),
 github_id bigint NOT NULL CHECK(github_id>0), imported_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,project_id,number),
 UNIQUE(workspace_id,project_id,item_node),
 FOREIGN KEY(workspace_id,project_id,number) REFERENCES planned_issue_state(workspace_id,project_id,number)
);
