-- Capture the target revision while the PR is open. After merge the target may
-- already point at the merge commit and cannot stand in for the tested base.
CREATE TABLE review_subjects(
 workspace_id text NOT NULL, project_id text NOT NULL, pull_number bigint NOT NULL,
 head text NOT NULL, base text NOT NULL, target text NOT NULL, captured_at timestamptz NOT NULL,
 PRIMARY KEY(workspace_id,project_id,pull_number,head,base,target),
 FOREIGN KEY(workspace_id,project_id,pull_number) REFERENCES pull_observations(workspace_id,project_id,number)
);
CREATE FUNCTION capture_review_subject() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.snapshot->>'state'='OPEN' AND NEW.snapshot->>'head' ~ '^[0-9a-f]{40}$' AND NEW.snapshot->>'base' ~ '^[0-9a-f]{40}$'
 AND nullif(NEW.snapshot->>'target','') IS NOT NULL THEN
 INSERT INTO review_subjects VALUES(NEW.workspace_id,NEW.project_id,NEW.number,NEW.snapshot->>'head',NEW.snapshot->>'base',NEW.snapshot->>'target',NEW.observed_at)
 ON CONFLICT(workspace_id,project_id,pull_number,head,base,target) DO UPDATE SET captured_at=excluded.captured_at;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER capture_review_subject AFTER INSERT OR UPDATE OF snapshot ON pull_observations
FOR EACH ROW EXECUTE FUNCTION capture_review_subject();
INSERT INTO review_subjects
 SELECT workspace_id,project_id,number,snapshot->>'head',snapshot->>'base',snapshot->>'target',observed_at FROM pull_observations
 WHERE snapshot->>'state'='OPEN' AND snapshot->>'head' ~ '^[0-9a-f]{40}$' AND snapshot->>'base' ~ '^[0-9a-f]{40}$' AND nullif(snapshot->>'target','') IS NOT NULL;
CREATE TABLE review_case_labels(
 workspace_id text NOT NULL,project_id text NOT NULL,pull_number bigint NOT NULL,head text NOT NULL,base text NOT NULL,target text NOT NULL,
 label text NOT NULL CHECK(label IN ('clean','regression','revert')),
 cohort text NOT NULL CHECK(cohort IN ('holdout','calibration')),reference text NOT NULL,
 evidence jsonb NOT NULL DEFAULT 'null'::jsonb, actor text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,project_id,pull_number,head,base,target),
 FOREIGN KEY(workspace_id,project_id,pull_number,head,base,target) REFERENCES review_subjects
);
