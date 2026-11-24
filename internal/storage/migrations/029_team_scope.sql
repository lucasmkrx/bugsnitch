ALTER TABLE workspace_members ADD COLUMN project_id text,
 ADD FOREIGN KEY (workspace_id,project_id) REFERENCES projects(workspace_id,id);
