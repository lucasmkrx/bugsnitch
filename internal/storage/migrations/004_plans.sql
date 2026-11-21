CREATE TABLE plan_versions(
 id text PRIMARY KEY, workspace_id text NOT NULL,project_id text NOT NULL,version integer NOT NULL,
 document jsonb NOT NULL,execution_order jsonb NOT NULL,approved_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id),UNIQUE(project_id,version)
);
ALTER TABLE admission ADD COLUMN plan_id text REFERENCES plan_versions(id);
ALTER TABLE admission ADD COLUMN resources jsonb NOT NULL DEFAULT '[]';
CREATE TABLE prerequisite_evidence(
 workspace_id text NOT NULL,project_id text NOT NULL,plan_id text NOT NULL REFERENCES plan_versions,
 issue_number integer NOT NULL,revision text NOT NULL,summary text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(plan_id,issue_number),FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id)
);
