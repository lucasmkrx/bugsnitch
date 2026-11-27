CREATE TABLE worker_starts (
 id text PRIMARY KEY, workspace_id text NOT NULL, project_id text NOT NULL, device_id text NOT NULL,
 issue_number integer NOT NULL CHECK(issue_number>0), request_id text NOT NULL, requested_plan text NOT NULL,
 policy_version bigint NOT NULL, github_id bigint NOT NULL, digest text NOT NULL, revision text NOT NULL,
 title text NOT NULL, plan_id text REFERENCES plan_versions(id), attempt_id text,
 status text NOT NULL CHECK(status IN ('queued','planning','awaiting_plan','starting','started','failed','expired')),
 reason text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
 UNIQUE(workspace_id,request_id), FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id),
 FOREIGN KEY(device_id,project_id) REFERENCES device_projects(device_id,project_id)
);
CREATE UNIQUE INDEX worker_starts_pending_issue ON worker_starts(workspace_id,project_id,issue_number)
 WHERE status IN ('queued','planning','awaiting_plan','starting');
CREATE INDEX worker_starts_queue ON worker_starts(device_id,project_id,created_at)
 WHERE status IN ('queued','planning','awaiting_plan','starting');
