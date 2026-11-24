ALTER TABLE pull_observations
 ADD COLUMN watching boolean NOT NULL DEFAULT false,
 ADD COLUMN next_watch timestamptz NOT NULL DEFAULT now();
CREATE INDEX pull_watch_due ON pull_observations(next_watch,workspace_id,project_id,number)
 WHERE watching AND snapshot->>'state'='OPEN';
