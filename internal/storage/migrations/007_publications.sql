CREATE TABLE publications(
 id text PRIMARY KEY,workspace_id text NOT NULL,project_id text NOT NULL, version bigint NOT NULL DEFAULT 1,
 request_id text NOT NULL, document jsonb NOT NULL,subject_version bigint NOT NULL,
 status text NOT NULL CHECK(status IN('proposed','publishing','published')),
 installation_id bigint,repository_id bigint,plan_id text REFERENCES plan_versions(id),created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,request_id), UNIQUE(workspace_id,project_id,id),
 FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id),
 FOREIGN KEY(workspace_id,project_id,installation_id,repository_id) REFERENCES github_mappings(workspace_id,project_id,installation_id,repository_id)
);
CREATE TABLE planned_issues(
 workspace_id text NOT NULL,project_id text NOT NULL,publication_id text NOT NULL,local_number integer NOT NULL,
 position integer NOT NULL,marker text NOT NULL UNIQUE,status text NOT NULL DEFAULT 'queued',
 canonical_number bigint,github_id bigint,source_state text,tries integer NOT NULL DEFAULT 0,
 reason text NOT NULL DEFAULT '',next_run timestamptz NOT NULL DEFAULT now(),created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(publication_id,local_number), FOREIGN KEY(workspace_id,project_id,publication_id) REFERENCES publications(workspace_id,project_id,id)
);
CREATE INDEX planned_queue ON planned_issues(next_run,publication_id,position) WHERE status IN ('queued','reconciling');
