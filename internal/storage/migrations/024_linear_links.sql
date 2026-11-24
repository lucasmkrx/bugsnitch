CREATE TABLE linear_issue_links(
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 issue_number int NOT NULL CHECK (issue_number>0),
 linear_id uuid NOT NULL,
 identifier text NOT NULL,
 title text NOT NULL,
 url text NOT NULL,
 state_name text NOT NULL,
 state_type text NOT NULL,
 source_updated_at timestamptz NOT NULL,
 checked_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,project_id,issue_number),
 UNIQUE(workspace_id,linear_id),
 FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id)
);
