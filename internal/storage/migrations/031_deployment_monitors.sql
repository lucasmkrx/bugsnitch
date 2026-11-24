-- A trusted publisher deploys the build and a separate check verifies the original behavior.
CREATE TABLE deployment_monitors (
    id text PRIMARY KEY, workspace_id text NOT NULL, project_id text NOT NULL, incident_id text NOT NULL,
    environment text NOT NULL, candidate text NOT NULL, deployer_id bigint NOT NULL, verifier_app_id bigint NOT NULL,
    check_name text NOT NULL, monitor_seconds integer NOT NULL, minimum_exposures integer NOT NULL,
    installation_id bigint NOT NULL, repository_id bigint NOT NULL, gate_hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(), active boolean NOT NULL DEFAULT true,
    FOREIGN KEY (workspace_id,project_id,incident_id) REFERENCES incidents(workspace_id,project_id,id),
    FOREIGN KEY (workspace_id,project_id,installation_id,repository_id)
        REFERENCES github_mappings(workspace_id,project_id,installation_id,repository_id)
);
CREATE UNIQUE INDEX deployment_monitors_active ON deployment_monitors(incident_id,environment) WHERE active;
CREATE INDEX deployment_monitors_repo ON deployment_monitors(repository_id,environment) WHERE active;

ALTER TABLE incident_evidence ADD COLUMN monitor_id text REFERENCES deployment_monitors(id),
    ADD COLUMN provider_order bigint NOT NULL DEFAULT 0;

-- A check webhook may arrive before the deployment-status webhook.
CREATE TABLE deployment_monitor_checks (
    monitor_id text NOT NULL REFERENCES deployment_monitors(id), run_id bigint NOT NULL, deployment_key text NOT NULL,
    build text NOT NULL, started_at timestamptz NOT NULL, completed_at timestamptz NOT NULL,
    positive boolean NOT NULL, coverage boolean, exposures bigint, applied boolean NOT NULL DEFAULT false,
    PRIMARY KEY (monitor_id,run_id)
);
CREATE INDEX deployment_checks_pending ON deployment_monitor_checks(monitor_id,deployment_key) WHERE NOT applied;

CREATE TABLE deployment_holds (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id text NOT NULL REFERENCES deployment_monitors(id), reason text NOT NULL,
    evidence_key text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), cleared_at timestamptz,
    UNIQUE (monitor_id,reason,evidence_key)
);
CREATE INDEX deployment_holds_active ON deployment_holds(monitor_id) WHERE cleared_at IS NULL;

-- Evaluate the latest deployed fix against signed Sentry observations and failed probes.
-- The project lock is shared by webhook writers. Reevaluating on either delivery order
-- also catches a recurrence received before its deployment notification.
CREATE FUNCTION record_deployment_holds(ws text, project text, env text) RETURNS void
LANGUAGE sql AS $$
    INSERT INTO deployment_holds(monitor_id,reason,evidence_key)
    SELECT m.id, evidence.reason, evidence.key
    FROM deployment_monitors m
    JOIN incidents i ON i.id=m.incident_id
    CROSS JOIN LATERAL (
       SELECT command_key,build,effective_at,monitor_id,candidate FROM incident_evidence
       WHERE incident_id=i.id AND kind='deployment' AND environment=m.environment
       ORDER BY effective_at DESC,provider_order DESC,received_at DESC,command_key DESC LIMIT 1
    ) d
    CROSS JOIN LATERAL (
       (SELECT 'current-build recurrence' AS reason, b.id::text AS key, b.accepted_at AS received
        FROM incident_inbox b WHERE (b.workspace_id,b.project_id,b.source,b.group_key)=(m.workspace_id,m.project_id,'sentry',i.group_key)
         AND i.source='sentry' AND b.environment=m.environment AND b.release=d.build AND b.seen_at>=d.effective_at
        ORDER BY b.seen_at DESC,b.id DESC LIMIT 1)
       UNION ALL
       (SELECT 'behavior check failed',v.command_key,v.received_at FROM incident_evidence v
        WHERE v.incident_id=i.id AND v.monitor_id=m.id AND v.kind='validation' AND v.deployment_key=d.command_key AND v.positive=false
        ORDER BY v.received_at DESC,v.provider_order DESC LIMIT 1)
    ) evidence
    WHERE m.active AND m.workspace_id=ws AND m.project_id=project AND m.environment=env
      AND d.monitor_id=m.id AND d.candidate=m.candidate
      AND NOT EXISTS (SELECT FROM deployment_holds h WHERE h.monitor_id=m.id AND h.reason=evidence.reason
          AND (h.cleared_at IS NULL OR h.cleared_at>=evidence.received))
    ON CONFLICT (monitor_id,reason,evidence_key) DO NOTHING
$$;

CREATE FUNCTION hold_after_sentry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM record_deployment_holds(NEW.workspace_id,NEW.project_id,NEW.environment);
    RETURN NULL;
END
$$;
CREATE TRIGGER deployment_monitor_recurrence AFTER INSERT ON incident_inbox
FOR EACH ROW WHEN (NEW.source='sentry' AND NEW.environment<>'' AND NEW.release<>'') EXECUTE FUNCTION hold_after_sentry();

ALTER TABLE deployment_monitors ADD COLUMN pull_number bigint, ADD COLUMN pull_version bigint,
  ADD COLUMN pull_criteria text, ADD COLUMN issue_digest text, ADD COLUMN fix_check text, ADD COLUMN fix_app_id bigint;

CREATE VIEW current_monitor_pull_subjects AS
SELECT m.id FROM deployment_monitors m
JOIN pull_observations o ON (o.workspace_id,o.project_id,o.number)=(m.workspace_id,m.project_id,m.pull_number)
JOIN incident_issues i ON (i.incident_id,i.number,i.repository_id,i.installation_id)=(m.incident_id,o.issue_number,m.repository_id,m.installation_id)
WHERE i.status='linked' AND i.requirements_digest=m.issue_digest AND o.snapshot->>'state'='MERGED'
  AND o.snapshot->>'mergedSha'=m.candidate AND o.snapshot->>'criteria'=m.pull_criteria;
