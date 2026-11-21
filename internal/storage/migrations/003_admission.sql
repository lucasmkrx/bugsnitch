ALTER TABLE workspaces ADD COLUMN ticket_limit integer NOT NULL DEFAULT 2 CHECK(ticket_limit BETWEEN 1 AND 32);
ALTER TABLE workspaces ADD COLUMN turn_limit integer NOT NULL DEFAULT 2 CHECK(turn_limit BETWEEN 1 AND 32);
ALTER TABLE devices ADD COLUMN turn_limit integer NOT NULL DEFAULT 2 CHECK(turn_limit BETWEEN 1 AND 32);
CREATE TABLE admission(
 workspace_id text NOT NULL, project_id text NOT NULL, device_id text NOT NULL, attempt_id text NOT NULL,
 issue_number integer NOT NULL CHECK(issue_number>0),base_sha text NOT NULL,
 sequence bigint NOT NULL,operation text NOT NULL, owns_issue boolean NOT NULL DEFAULT true, active boolean NOT NULL, in_turn boolean NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(workspace_id,attempt_id),
 FOREIGN KEY(workspace_id,device_id) REFERENCES devices(workspace_id,id),
 FOREIGN KEY(device_id,project_id) REFERENCES device_projects(device_id,project_id),
 FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id),CHECK(NOT in_turn OR active)
);
CREATE UNIQUE INDEX admission_one_issue ON admission(workspace_id,project_id,issue_number) WHERE owns_issue;
