ALTER TABLE planned_issues ADD COLUMN creation_digest text;
-- Observed GitHub identity stays bound to the original installation and repository.
CREATE TABLE planned_issue_state(
 workspace_id text NOT NULL, project_id text NOT NULL, number bigint NOT NULL, github_id bigint NOT NULL,
 installation_id bigint NOT NULL, repository_id bigint NOT NULL,
 generation bigint NOT NULL DEFAULT 1, title text NOT NULL DEFAULT '', digest text NOT NULL DEFAULT '',
 source_state text NOT NULL DEFAULT '', availability text NOT NULL DEFAULT 'pending' CHECK(availability IN ('current','pending','unknown')),
 reason text NOT NULL DEFAULT '', checked_at timestamptz, next_check timestamptz NOT NULL DEFAULT now(), failures integer NOT NULL DEFAULT 0,
 PRIMARY KEY(workspace_id,project_id,number), UNIQUE(workspace_id,project_id,github_id),
 FOREIGN KEY(workspace_id,project_id,installation_id,repository_id) REFERENCES github_mappings(workspace_id,project_id,installation_id,repository_id)
);
CREATE INDEX planned_drift_queue ON planned_issue_state(next_check);
INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,reason)
 SELECT i.workspace_id,i.project_id,i.canonical_number,i.github_id,x.installation_id,x.repository_id,'Awaiting first canonical read'
 FROM planned_issues i JOIN publications x ON i.publication_id=x.id WHERE i.status='linked' ON CONFLICT DO NOTHING;
CREATE TABLE plan_issue_bindings(
 workspace_id text NOT NULL,project_id text NOT NULL,plan_id text NOT NULL REFERENCES plan_versions(id),number bigint NOT NULL,generation bigint NOT NULL,
 PRIMARY KEY(plan_id,number),FOREIGN KEY(workspace_id,project_id,number) REFERENCES planned_issue_state(workspace_id,project_id,number)
);
CREATE TABLE planned_issue_signals(
 workspace_id text NOT NULL,project_id text NOT NULL,github_id bigint NOT NULL,signal_hash text NOT NULL,accepted_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,project_id,github_id,signal_hash),
 FOREIGN KEY(workspace_id,project_id,github_id) REFERENCES planned_issue_state(workspace_id,project_id,github_id)
);

CREATE FUNCTION request_issue_drift_pause() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.generation <> OLD.generation THEN
    INSERT INTO events(workspace_id,project_id,kind,subject_id)
     VALUES(NEW.workspace_id,NEW.project_id,'plan.issue_invalidated',NEW.number::text);
    INSERT INTO controls(id,workspace_id,project_id,attempt_id,device_id,operation,subject_version,request_id,status)
     SELECT gen_random_uuid()::text,a.workspace_id,a.project_id,a.id,a.device_id,'pause',a.version,
       'issue_drift_'||a.id||'_'||NEW.generation,'requested'
     FROM attempt_reports a
     WHERE a.workspace_id=NEW.workspace_id AND a.project_id=NEW.project_id AND a.issue_number=NEW.number AND a.state='running'
     ON CONFLICT DO NOTHING;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER pause_on_issue_drift AFTER UPDATE OF generation ON planned_issue_state
 FOR EACH ROW EXECUTE FUNCTION request_issue_drift_pause();
