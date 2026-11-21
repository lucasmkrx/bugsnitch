CREATE TABLE controls(
 id text PRIMARY KEY,workspace_id text NOT NULL,project_id text NOT NULL,attempt_id text NOT NULL,device_id text NOT NULL,
 operation text NOT NULL CHECK(operation IN ('pause','stop')), subject_version bigint NOT NULL,
 status text NOT NULL CHECK(status IN('requested','received','completed','superseded','expired','unavailable')),
 request_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
 FOREIGN KEY(workspace_id,attempt_id) REFERENCES attempt_reports(workspace_id,id),
 FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id),
 FOREIGN KEY(workspace_id,device_id) REFERENCES devices(workspace_id,id),
 UNIQUE(workspace_id,request_id)
);
CREATE UNIQUE INDEX control_pending ON controls(workspace_id,attempt_id) WHERE status IN('requested','received');
ALTER TABLE attempt_reports ADD COLUMN evidence jsonb NOT NULL DEFAULT '[]';
