CREATE TABLE github_project_status (
    workspace_id text NOT NULL, project_id text NOT NULL, number bigint NOT NULL,
    desired text NOT NULL CHECK(desired IN ('active','done')), applied text,
    failures integer NOT NULL DEFAULT 0, next_run timestamptz NOT NULL DEFAULT now(), reason text NOT NULL DEFAULT '',
    PRIMARY KEY(workspace_id,project_id,number),
    FOREIGN KEY(workspace_id,project_id,number) REFERENCES github_project_tickets ON DELETE CASCADE
);
CREATE INDEX github_project_status_due ON github_project_status(next_run) WHERE applied IS DISTINCT FROM desired;

CREATE FUNCTION queue_project_status() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ticket bigint; source_id bigint; stage text;
BEGIN
    IF TG_TABLE_NAME='worker_starts' THEN
        ticket := NEW.issue_number;
        source_id := NEW.github_id;
        stage := 'active';
    ELSE
        SELECT issue_number INTO ticket FROM pull_observations
          WHERE (workspace_id,project_id,number)=(NEW.workspace_id,NEW.project_id,NEW.pull_number)
            AND version=NEW.inspection_version
            AND snapshot->>'state'='MERGED' AND snapshot->>'mergedSha'=NEW.merged_sha AND snapshot->>'target'=NEW.target
            AND (NOT EXISTS(SELECT FROM deployment_monitors m WHERE (m.workspace_id,m.project_id,m.pull_number,m.candidate)=
                     (NEW.workspace_id,NEW.project_id,NEW.pull_number,NEW.merged_sha) AND m.active)
                 OR EXISTS(SELECT FROM deployment_monitors m WHERE (m.workspace_id,m.project_id,m.pull_number,m.candidate,m.environment)=
                     (NEW.workspace_id,NEW.project_id,NEW.pull_number,NEW.merged_sha,NEW.environment) AND m.active));
        stage := 'done';
    END IF;
    INSERT INTO github_project_status(workspace_id,project_id,number,desired)
        SELECT workspace_id,project_id,number,stage FROM github_project_tickets
        WHERE (workspace_id,project_id,number)=(NEW.workspace_id,NEW.project_id,ticket)
          AND (source_id IS NULL OR github_id=source_id)
        ON CONFLICT(workspace_id,project_id,number) DO UPDATE SET desired=excluded.desired,failures=0,reason='',next_run=now()
        WHERE github_project_status.desired<>excluded.desired;
    RETURN NULL;
END $$;

CREATE TRIGGER worker_project_status AFTER UPDATE OF status ON worker_starts
    FOR EACH ROW WHEN (OLD.status='queued' AND NEW.status IN ('planning','starting')) EXECUTE FUNCTION queue_project_status();
CREATE TRIGGER verified_project_status AFTER INSERT ON pull_handovers
    FOR EACH ROW EXECUTE FUNCTION queue_project_status();
