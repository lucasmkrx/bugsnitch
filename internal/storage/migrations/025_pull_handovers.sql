CREATE TABLE pull_handovers(
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 pull_number bigint NOT NULL,
 inspection_version bigint NOT NULL,
 merged_sha text NOT NULL,
 target text NOT NULL,
 environment text NOT NULL,
 build text NOT NULL,
 evidence_digest text NOT NULL,
 summary text NOT NULL,
 test_steps text NOT NULL,
 actor_role text NOT NULL CHECK (actor_role IN ('owner','approver')),
 actor_id text NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,project_id,pull_number,environment,build),
 FOREIGN KEY(workspace_id,project_id,pull_number) REFERENCES pull_observations(workspace_id,project_id,number)
);
