-- Keep revisions and inconclusive provider results from certifying a deployment.
ALTER TABLE deployment_monitor_checks
  ADD COLUMN disputed boolean NOT NULL DEFAULT false,
  ADD COLUMN inconclusive boolean NOT NULL DEFAULT false,
  ADD COLUMN confirmed_failure boolean NOT NULL DEFAULT false;
UPDATE deployment_monitor_checks SET confirmed_failure=true WHERE NOT positive;
CREATE TABLE deployment_fix_checks (
  monitor_id text NOT NULL REFERENCES deployment_monitors(id), run_id bigint NOT NULL,
  completed_at timestamptz NOT NULL, positive boolean NOT NULL, disputed boolean NOT NULL DEFAULT false,
  PRIMARY KEY(monitor_id,run_id)
);
INSERT INTO deployment_fix_checks(monitor_id,run_id,completed_at,positive)
  SELECT monitor_id,provider_order,effective_at,true FROM incident_evidence
  WHERE kind='fix' AND monitor_id IS NOT NULL;

CREATE INDEX deployment_fix_order ON deployment_fix_checks(monitor_id,completed_at DESC,run_id DESC);
CREATE INDEX deployment_check_order ON deployment_monitor_checks(monitor_id,deployment_key,completed_at DESC,run_id DESC);

-- Receipt timestamps must follow acquisition of the project lock. Transactions that
-- began before a clear can acquire that lock afterwards, and delayed observations
-- still qualify when their event time falls inside the current deployment.
ALTER TABLE incident_inbox ALTER COLUMN accepted_at SET DEFAULT clock_timestamp();
ALTER TABLE incident_evidence ALTER COLUMN received_at SET DEFAULT clock_timestamp();
CREATE OR REPLACE FUNCTION record_deployment_holds(ws text, project text, env text) RETURNS void
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
        ORDER BY b.accepted_at DESC,b.id DESC LIMIT 1)
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

-- A successful deployment can subsequently be marked inactive or failed. Status IDs
-- break ties when GitHub's second-precision timestamps are identical.
CREATE TABLE deployment_activation_states (
  monitor_id text NOT NULL REFERENCES deployment_monitors(id), deployment_id bigint NOT NULL,
  status_id bigint NOT NULL, succeeded boolean NOT NULL, effective_at timestamptz NOT NULL,
  PRIMARY KEY (monitor_id,deployment_id,status_id)
);
INSERT INTO deployment_activation_states(monitor_id,deployment_id,status_id,succeeded,effective_at)
  SELECT monitor_id,split_part(command_key,'-',3)::bigint,provider_order,true,effective_at
  FROM incident_evidence WHERE kind='deployment' AND monitor_id IS NOT NULL AND command_key ~ '^gh-deploy-[0-9]+-[0-9]+$';
CREATE INDEX deployment_activation_latest ON deployment_activation_states(monitor_id,deployment_id,effective_at DESC,status_id DESC);
