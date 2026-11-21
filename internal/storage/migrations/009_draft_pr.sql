ALTER TABLE attempt_reports ADD COLUMN issue_updated_at text NOT NULL DEFAULT '';
CREATE TABLE draft_pr_intents(
 workspace_id text NOT NULL,project_id text NOT NULL,attempt_id text NOT NULL,report_version bigint NOT NULL,policy_version bigint NOT NULL,
 installation_id bigint NOT NULL,repository_id bigint NOT NULL,issue_number bigint NOT NULL,issue_updated_at text NOT NULL,
 candidate_sha text NOT NULL,base_sha text NOT NULL,target text NOT NULL,marker text NOT NULL UNIQUE,
 status text NOT NULL CHECK(status IN ('queued','reconciling','created','blocked')), reason text NOT NULL DEFAULT '',
 tries integer NOT NULL DEFAULT 0,next_run timestamptz NOT NULL DEFAULT now(),number bigint,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,attempt_id),UNIQUE(repository_id,number),
 FOREIGN KEY(workspace_id,attempt_id) REFERENCES attempt_reports(workspace_id,id),
 FOREIGN KEY(workspace_id,project_id,installation_id,repository_id) REFERENCES github_mappings(workspace_id,project_id,installation_id,repository_id)
);
CREATE INDEX draft_pr_pending ON draft_pr_intents(next_run) WHERE status IN('queued','reconciling');
ALTER TABLE draft_pr_intents ADD COLUMN summary text NOT NULL DEFAULT '', ADD COLUMN behavior text NOT NULL DEFAULT '';
