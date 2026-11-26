CREATE TABLE incident_browser_qa (
    workspace_id text NOT NULL, project_id text NOT NULL, incident_id text NOT NULL,
    attempt_id text NOT NULL, candidate_sha text NOT NULL, checked_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(), digest text NOT NULL, report jsonb NOT NULL,
    PRIMARY KEY (incident_id,attempt_id,candidate_sha),
    FOREIGN KEY (workspace_id,project_id,incident_id) REFERENCES incidents(workspace_id,project_id,id)
);
CREATE INDEX incident_browser_qa_latest ON incident_browser_qa(incident_id,checked_at DESC);
