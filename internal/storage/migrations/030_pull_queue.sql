CREATE TABLE pull_queue_permits(
 workspace_id text NOT NULL,project_id text NOT NULL,number bigint NOT NULL,version bigint NOT NULL,
 policy_version bigint NOT NULL,plan_version bigint NOT NULL,installation_id bigint NOT NULL,repository_id bigint NOT NULL,
 head text NOT NULL,base text NOT NULL,target text NOT NULL,criteria text NOT NULL,rules_digest text NOT NULL,
 issue_number bigint NOT NULL,actor_id text NOT NULL,environment text NOT NULL,
 state text NOT NULL CHECK(state IN ('requested','submitting','awaiting_queue','queued','checking','passed','held','revoked')),
 attempt_id text NOT NULL,reason text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '1 hour',group_sha text NOT NULL DEFAULT '',
 PRIMARY KEY(workspace_id,project_id,number,version),
 FOREIGN KEY(workspace_id,attempt_id) REFERENCES attempt_reports(workspace_id,id),
 FOREIGN KEY(workspace_id,project_id,number) REFERENCES pull_observations(workspace_id,project_id,number),
 FOREIGN KEY(workspace_id,project_id,installation_id,repository_id) REFERENCES github_mappings(workspace_id,project_id,installation_id,repository_id)
);
CREATE UNIQUE INDEX one_active_queue_permit ON pull_queue_permits(workspace_id,project_id,number)
 WHERE state IN ('requested','submitting','awaiting_queue','queued','checking');
CREATE TABLE github_merge_groups(
 workspace_id text NOT NULL,project_id text NOT NULL,installation_id bigint NOT NULL,repository_id bigint NOT NULL,
 delivery text NOT NULL,head text NOT NULL,base text NOT NULL,head_ref text NOT NULL,target text NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,project_id,delivery),
 FOREIGN KEY(workspace_id,project_id,installation_id,repository_id) REFERENCES github_mappings(workspace_id,project_id,installation_id,repository_id)
);
CREATE INDEX recent_merge_group ON github_merge_groups(workspace_id,project_id,target,base,observed_at DESC);
