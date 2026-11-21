CREATE TABLE general_model_turns (
 workspace_id text NOT NULL REFERENCES workspaces,
 device_id text NOT NULL,
 permit_id text NOT NULL,
 role text NOT NULL CHECK(role IN ('planner','manager','proof')),
 project_id text REFERENCES projects,
 version integer NOT NULL CHECK(version IN (1,2)),
 created_at timestamptz NOT NULL DEFAULT now(),
 ended_at timestamptz,
 PRIMARY KEY(workspace_id,permit_id),
 FOREIGN KEY(workspace_id,device_id) REFERENCES devices(workspace_id,id),
 FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id),
 CHECK ((version=1 AND ended_at IS NULL) OR (version=2 AND ended_at IS NOT NULL))
);
CREATE INDEX general_turns_active ON general_model_turns(workspace_id,device_id) WHERE version=1;
