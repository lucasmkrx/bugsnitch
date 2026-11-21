-- Unavailable attribution stays NULL for pre-migration events.
ALTER TABLE events
 ADD COLUMN actor_source text CHECK (actor_source IN ('owner','plugin','device','service','provider')),
 ADD COLUMN actor_id text CHECK (actor_id IS NULL OR (length(actor_id) BETWEEN 1 AND 160 AND actor_source IS NOT NULL)),
 ADD COLUMN reason text CHECK (length(reason) BETWEEN 1 AND 320),
 ADD COLUMN policy_version bigint CHECK (policy_version > 0),
 ADD COLUMN subject_version bigint CHECK (subject_version > 0),
 ADD COLUMN evidence_ref text CHECK (length(evidence_ref) BETWEEN 1 AND 160);

CREATE OR REPLACE FUNCTION request_issue_drift_pause() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.generation <> OLD.generation THEN
    INSERT INTO events(workspace_id,project_id,kind,subject_id,actor_source,actor_id,reason,subject_version)
     VALUES(NEW.workspace_id,NEW.project_id,'plan.issue_invalidated',NEW.number::text,'service','issue-drift-trigger',
            'The canonical issue generation changed',NEW.generation);
    INSERT INTO controls(id,workspace_id,project_id,attempt_id,device_id,operation,subject_version,request_id,status)
     SELECT gen_random_uuid()::text,a.workspace_id,a.project_id,a.id,a.device_id,'pause',a.version,
       'issue_drift_'||a.id||'_'||NEW.generation,'requested'
     FROM attempt_reports a
     WHERE a.workspace_id=NEW.workspace_id AND a.project_id=NEW.project_id AND a.issue_number=NEW.number AND a.state='running'
     ON CONFLICT DO NOTHING;
  END IF;
  RETURN NEW;
END $$;
