ALTER TABLE devices ADD COLUMN previous_hash text,ADD COLUMN rotated_at timestamptz;
CREATE TABLE retention_runs(id boolean PRIMARY KEY DEFAULT true CHECK(id),last_run timestamptz NOT NULL);
ALTER TABLE incident_inbox ADD COLUMN redacted_at timestamptz;
ALTER TABLE incidents ADD COLUMN redacted_at timestamptz;
