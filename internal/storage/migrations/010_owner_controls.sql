ALTER TABLE workspaces ADD COLUMN stopped boolean NOT NULL DEFAULT false,
 ADD COLUMN dispatch_version bigint NOT NULL DEFAULT 1;
ALTER TABLE projects ADD COLUMN retention_days integer NOT NULL DEFAULT 30 CHECK(retention_days BETWEEN 1 AND 3650);
ALTER TABLE manager_messages ADD COLUMN redacted_at timestamptz;
CREATE TABLE shared_diffs(
 workspace_id text NOT NULL,project_id text NOT NULL,attempt_id text NOT NULL,device_id text NOT NULL,version bigint NOT NULL,
 base_sha text NOT NULL,candidate_sha text NOT NULL,content text NOT NULL,digest text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,attempt_id),
 FOREIGN KEY(workspace_id,attempt_id) REFERENCES attempt_reports(workspace_id,id),
 FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id),
 FOREIGN KEY(workspace_id,device_id) REFERENCES devices(workspace_id,id)
);
CREATE TABLE workspace_controls(workspace_id text NOT NULL REFERENCES workspaces,request_id text NOT NULL,
 stopped boolean NOT NULL,expected bigint NOT NULL,result bigint NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,request_id));
