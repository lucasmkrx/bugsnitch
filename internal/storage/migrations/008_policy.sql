ALTER TABLE projects ADD COLUMN preset text NOT NULL DEFAULT 'Supervised',ADD COLUMN policy_version bigint NOT NULL DEFAULT 1,
 ADD COLUMN require_plan boolean NOT NULL DEFAULT false,ADD COLUMN allow_publication boolean NOT NULL DEFAULT true,
 ADD COLUMN allow_promotion boolean NOT NULL DEFAULT true,ADD COLUMN allow_draft_pr boolean NOT NULL DEFAULT true,
 ADD COLUMN auto_triage boolean NOT NULL DEFAULT false;
CREATE TABLE policy_versions(
 workspace_id text NOT NULL,project_id text NOT NULL,version bigint NOT NULL,actor text NOT NULL,policy jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(project_id,version),FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id)
);
ALTER TABLE incident_issues ADD COLUMN policy_version bigint NOT NULL DEFAULT 1;
ALTER TABLE publications ADD COLUMN policy_version bigint;
ALTER TABLE workspaces ADD COLUMN limits_version bigint NOT NULL DEFAULT 1;
