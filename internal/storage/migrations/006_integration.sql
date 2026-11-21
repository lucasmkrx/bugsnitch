CREATE TABLE pull_observations(
 workspace_id text NOT NULL,project_id text NOT NULL,number bigint NOT NULL,issue_number bigint NOT NULL,
 version bigint NOT NULL, snapshot jsonb NOT NULL,policy_version bigint NOT NULL,plan_version integer NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,project_id,number),
 FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id)
);
CREATE TABLE pull_evidence(
 workspace_id text NOT NULL,project_id text NOT NULL,number bigint NOT NULL,version bigint NOT NULL,
 kind text NOT NULL CHECK(kind IN ('combined_validation','independent_review')),
 reference text NOT NULL, recorded_at timestamptz NOT NULL DEFAULT now(),source text NOT NULL DEFAULT 'authenticated owner',
 PRIMARY KEY(workspace_id,project_id,number,version,kind),
 FOREIGN KEY(workspace_id,project_id,number) REFERENCES pull_observations(workspace_id,project_id,number)
);
