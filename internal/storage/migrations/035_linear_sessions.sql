CREATE TABLE linear_sessions(
 id uuid PRIMARY KEY, workspace_id text NOT NULL,project_id text NOT NULL,issue_number int NOT NULL,
 latest_prompt uuid, last_digest text, finished boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(), due_at timestamptz NOT NULL DEFAULT now(),
 failures int NOT NULL DEFAULT 0,
 FOREIGN KEY(workspace_id,project_id,issue_number) REFERENCES linear_issue_links(workspace_id,project_id,issue_number)
);
CREATE INDEX linear_sessions_due ON linear_sessions(due_at) WHERE NOT finished;
CREATE INDEX linear_sessions_issue ON linear_sessions(workspace_id,project_id,issue_number);
CREATE TABLE linear_prompts(session_id uuid NOT NULL REFERENCES linear_sessions,id uuid NOT NULL,PRIMARY KEY(session_id,id));
