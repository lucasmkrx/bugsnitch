-- A validation belongs to one deployment, even when the same artifact is deployed twice.
ALTER TABLE incident_evidence ADD COLUMN deployment_key text,
    ADD FOREIGN KEY (workspace_id,project_id,incident_id,deployment_key)
        REFERENCES incident_evidence(workspace_id,project_id,incident_id,command_key);

-- Legacy receipts had no deployment key. Retain their effect only when there
-- is one possible deployment of this artifact that predates the validation.
WITH unambiguous AS (
    SELECT v.workspace_id,v.project_id,v.incident_id,v.command_key,min(d.command_key) AS deployment_key
    FROM incident_evidence v
    JOIN incident_evidence d USING (workspace_id,project_id,incident_id,environment,build,candidate)
    WHERE v.kind='validation' AND d.kind='deployment' AND d.effective_at<=v.effective_at
    GROUP BY v.workspace_id,v.project_id,v.incident_id,v.command_key,v.received_at
    HAVING count(*)=1 AND min(d.received_at)<v.received_at
)
UPDATE incident_evidence v SET deployment_key=u.deployment_key FROM unambiguous u
WHERE (v.workspace_id,v.project_id,v.incident_id,v.command_key)=(u.workspace_id,u.project_id,u.incident_id,u.command_key);
