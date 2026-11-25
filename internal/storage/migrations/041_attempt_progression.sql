CREATE TABLE ticket_decisions (
    workspace_id text NOT NULL,
    attempt_id text NOT NULL,
    attempt_version bigint NOT NULL CHECK(attempt_version>0),
    kind text NOT NULL CHECK(kind IN ('implementation_plan','publish_correction')),
    subject jsonb NOT NULL DEFAULT '{}'::jsonb,
    recipient text NOT NULL,
    subject_policy bigint NOT NULL,
    plan_id text NOT NULL,
    expires_at timestamptz NOT NULL,
    answer text CHECK(answer IN ('approve','hold')),
    answered_at timestamptz,
    PRIMARY KEY(workspace_id,attempt_id,attempt_version,kind),
    FOREIGN KEY(workspace_id,attempt_id) REFERENCES attempt_reports(workspace_id,id) ON DELETE CASCADE
);

CREATE FUNCTION expire_moved_correction_decisions() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ticket_decisions q SET expires_at=now()
    FROM attempt_reports a
    WHERE (q.workspace_id,q.attempt_id,q.attempt_version)=(a.workspace_id,a.id,a.version)
      AND (a.workspace_id,a.project_id)=(NEW.workspace_id,NEW.project_id)
      AND q.kind='publish_correction' AND (q.subject->>'pullNumber')::bigint=NEW.number
      AND q.expires_at>now() AND NEW.snapshot->>'head' IS DISTINCT FROM a.candidate_sha
      AND (NEW.snapshot->>'head' IS DISTINCT FROM q.subject->>'previousHead'
        OR NEW.snapshot->>'base' IS DISTINCT FROM q.subject->>'baseSha'
        OR NEW.snapshot->>'target' IS DISTINCT FROM q.subject->>'target'
        OR NEW.snapshot->>'state' IS DISTINCT FROM 'OPEN');
    RETURN NULL;
END $$;
CREATE TRIGGER expire_moved_correction_decisions AFTER UPDATE OF snapshot ON pull_observations
 FOR EACH ROW WHEN (OLD.snapshot IS DISTINCT FROM NEW.snapshot) EXECUTE FUNCTION expire_moved_correction_decisions();

ALTER TABLE draft_pr_intents ADD COLUMN follow_at timestamptz;
ALTER TABLE pull_observations ADD COLUMN progression_signal bigint NOT NULL DEFAULT 0;
CREATE INDEX draft_pr_follow_due ON draft_pr_intents(follow_at) WHERE status='created' AND follow_at IS NOT NULL;
CREATE TRIGGER wake_created_draft_watch AFTER UPDATE OF status,follow_at ON draft_pr_intents
 FOR EACH ROW WHEN (NEW.status='created' AND NEW.follow_at<=clock_timestamp()) EXECUTE FUNCTION wake_background_jobs();
CREATE TRIGGER wake_pr_inspection AFTER UPDATE OF next_watch ON pull_observations
 FOR EACH ROW WHEN (NEW.watching AND NEW.next_watch<=clock_timestamp()) EXECUTE FUNCTION wake_background_jobs();

CREATE FUNCTION wake_ticket_progression() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_catalog.pg_notify('bugsnitch_jobs_' || pg_catalog.md5(TG_TABLE_SCHEMA), NEW.project_id);
    RETURN NULL;
END $$;
CREATE TRIGGER wake_ticket_progression AFTER UPDATE OF position ON events
    FOR EACH ROW WHEN (NEW.project_id IS NOT NULL AND OLD.position IS NULL AND NEW.position IS NOT NULL)
    EXECUTE FUNCTION wake_ticket_progression();
CREATE INDEX events_project_position ON events(workspace_id,project_id,position DESC);

CREATE TABLE prepared_signoffs (
    workspace_id text NOT NULL, attempt_id text NOT NULL, attempt_version bigint NOT NULL,
    pull_number bigint NOT NULL, inspection_version bigint NOT NULL,
    candidate_sha text NOT NULL, base_sha text NOT NULL,
    independent_review text NOT NULL, combined_validation text NOT NULL,
    PRIMARY KEY(workspace_id,attempt_id,attempt_version,pull_number,inspection_version),
    FOREIGN KEY(workspace_id,attempt_id) REFERENCES attempt_reports(workspace_id,id) ON DELETE CASCADE
);

CREATE TABLE attempt_recovery_holds (
    workspace_id text NOT NULL, attempt_id text NOT NULL, attempt_version bigint NOT NULL,
    PRIMARY KEY(workspace_id,attempt_id,attempt_version),
    FOREIGN KEY(workspace_id,attempt_id) REFERENCES attempt_reports(workspace_id,id) ON DELETE CASCADE
);

-- The decision returned to the owning device is the one displayed in Work.
-- Execution still rechecks the exact subject through the existing operation.
CREATE VIEW attempt_progression AS
SELECT a.workspace_id,a.project_id,a.device_id,a.id,a.version,
    jsonb_strip_nulls(jsonb_build_object(
        'attemptId',a.id,'version',a.version,'candidateSha',a.candidate_sha,'workspaceId',a.workspace_id,'projectId',a.project_id,
        'kind',decision.kind,
        'reason',CASE decision.kind
            WHEN 'device_revoked' THEN 'The owner must enroll a device and review the retained attempt.'
            WHEN 'device_offline' THEN 'The originating runner must reconnect and report its current checkpoint.'
            WHEN 'dispatch_paused' THEN 'The workspace or project must resume dispatch.'
            WHEN 'stage_running' THEN 'Waiting for the originating device to report the stage result.'
            WHEN 'inspect_execution' THEN 'Inspect the journal and worktree on the originating device.'
            WHEN 'wait_draft' THEN 'Waiting for the GitHub App to report the draft receipt.'
            WHEN 'wait_inspection' THEN 'Waiting for the first authoritative GitHub inspection.'
            WHEN 'wait_ci' THEN 'Waiting for checks on the inspected PR head.'
            WHEN 'wait_deployment' THEN 'Waiting for evidence on the active monitored build.'
            WHEN 'inspect_revision' THEN 'The observed GitHub revision or target changed. Review the new subject.'
            WHEN 'hold' THEN nullif(x.reason,'')
        END,
        'disposition',CASE
            WHEN decision.kind IN ('publish_draft','publish_correction','resume_candidate') THEN 'act'
            WHEN decision.kind IN ('review_plan','owner_input','review_correction','inspect_execution',
                'approve_draft','approve_correction','prepare_signoff','deployment_handover','inspect_revision','hold') THEN 'ask'
            WHEN decision.kind IN ('finished','terminal') THEN 'finish'
            ELSE 'wait'
        END,
        'approver',CASE decision.kind
            WHEN 'review_plan' THEN coalesce(q.recipient,w.account_id)
            WHEN 'approve_correction' THEN q.recipient
            WHEN 'approve_draft' THEN w.account_id
            WHEN 'prepare_signoff' THEN 'Owner or scoped approver'
            WHEN 'deployment_handover' THEN 'Owner or scoped approver'
        END,
        'expiresAt',q.expires_at,
        'answer',CASE WHEN live_decision.active THEN q.answer END,
        'decisionKind',q.kind,'decisionSubject',q.subject,
        'draft',CASE WHEN x.attempt_id IS NOT NULL AND x.local_ship THEN jsonb_build_object(
            'target',x.target,'status',x.status,'number',x.number,'approvalVersion',x.approval_version) END,
        'pull',CASE WHEN o.number IS NOT NULL THEN jsonb_build_object(
            'number',o.number,'version',o.version,'head',o.snapshot->>'head',
            'base',o.snapshot->>'base','target',o.snapshot->>'target','checks',o.snapshot->>'checks',
            'review',o.snapshot->>'review','state',o.snapshot->>'state','draft',o.snapshot->'draft',
            'watching',o.watching,'signal',o.progression_signal,'mergedSha',o.snapshot->>'mergedSha') END,
        'deployment',monitor.evidence,
        'preparedEvidence',CASE WHEN s.attempt_id IS NOT NULL THEN jsonb_build_object(
            'pullNumber',s.pull_number,'inspectionVersion',s.inspection_version,'baseSha',s.base_sha,
            'independentReview',s.independent_review,'combinedValidation',s.combined_validation) END
    )) AS action
FROM attempt_reports a
JOIN projects p ON (p.workspace_id,p.id)=(a.workspace_id,a.project_id)
JOIN workspaces w ON w.id=a.workspace_id
JOIN devices d ON (d.workspace_id,d.id)=(a.workspace_id,a.device_id)
LEFT JOIN LATERAL (SELECT id FROM plan_versions v
    WHERE (v.workspace_id,v.project_id)=(a.workspace_id,a.project_id) AND v.approved_at IS NOT NULL
    ORDER BY v.version DESC LIMIT 1) plan ON true
LEFT JOIN ticket_decisions q ON (q.workspace_id,q.attempt_id,q.attempt_version)
    =(a.workspace_id,a.id,a.version)
    AND (q.kind='implementation_plan' AND a.state='waiting' OR q.kind='publish_correction' AND a.state='completed')
LEFT JOIN LATERAL (SELECT q.expires_at>now() AND q.subject_policy=p.policy_version AND q.plan_id=coalesce(plan.id,'')
    AND (q.recipient=w.account_id OR EXISTS(SELECT 1 FROM workspace_members m WHERE m.workspace_id=q.workspace_id
        AND m.id=q.recipient AND m.role='approver' AND m.revoked_at IS NULL AND (m.project_id IS NULL OR m.project_id=p.id)))
    AS active) live_decision ON true
LEFT JOIN draft_pr_intents x ON (x.workspace_id,x.project_id,x.attempt_id,x.report_version,x.candidate_sha)
    =(a.workspace_id,a.project_id,a.id,a.version,a.candidate_sha)
LEFT JOIN pull_observations o ON (o.workspace_id,o.project_id,o.number)
    =(a.workspace_id,a.project_id,coalesce(x.number,(q.subject->>'pullNumber')::bigint))
LEFT JOIN prepared_signoffs s ON (s.workspace_id,s.attempt_id,s.attempt_version,s.pull_number,s.inspection_version,s.candidate_sha,s.base_sha)
    =(a.workspace_id,a.id,a.version,o.number,o.version,o.snapshot->>'head',o.snapshot->>'base')
LEFT JOIN attempt_recovery_holds h ON (h.workspace_id,h.attempt_id,h.attempt_version)=(a.workspace_id,a.id,a.version)
LEFT JOIN LATERAL (
    SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
        'incidentId',m.incident_id,'environment',m.environment,'candidateSha',m.candidate,'monitorId',m.id,
        'held',EXISTS(SELECT 1 FROM deployment_holds h WHERE h.monitor_id=m.id AND h.cleared_at IS NULL),
        'build',current_deployment.build,'deploymentKey',current_deployment.command_key
    )) ORDER BY m.environment,m.id) AS evidence
    FROM deployment_monitors m
    LEFT JOIN LATERAL (SELECT e.command_key,e.build,e.candidate,e.monitor_id
        FROM incident_evidence e WHERE e.incident_id=m.incident_id AND e.kind='deployment'
            AND e.environment=m.environment
        ORDER BY e.effective_at DESC,e.provider_order DESC,e.received_at DESC,e.command_key DESC LIMIT 1
    ) current_deployment ON current_deployment.candidate=m.candidate AND current_deployment.monitor_id=m.id
    WHERE (m.workspace_id,m.project_id,m.pull_number)=(o.workspace_id,o.project_id,o.number)
        AND m.candidate=o.snapshot->>'mergedSha' AND m.active
) monitor ON o.snapshot->>'state'='MERGED'
CROSS JOIN LATERAL (SELECT CASE
    WHEN d.revoked THEN 'device_revoked'
    WHEN w.stopped OR p.paused THEN 'dispatch_paused'
    WHEN a.state IN ('failed','cancelled') THEN 'terminal'
    WHEN a.state<>'completed' AND d.last_contact<now()-interval '2 minutes' THEN 'device_offline'
    WHEN a.state='running' AND h.attempt_id IS NOT NULL THEN 'inspect_execution'
    WHEN a.state='running' THEN 'stage_running'
    WHEN a.state='waiting' THEN CASE a.reason_code
        WHEN 'plan_review_required' THEN 'review_plan'
        WHEN 'owner_input_required' THEN 'owner_input'
        WHEN 'review_correction' THEN 'review_correction'
        WHEN 'recovered_candidate' THEN 'resume_candidate'
        ELSE 'inspect_execution' END
    WHEN a.state='completed' AND x.status='awaiting_publication' AND x.local_ship
        AND p.allow_draft_pr AND x.policy_version=p.policy_version THEN 'publish_draft'
    WHEN a.state='completed' AND x.status IN ('queued','reconciling') THEN 'wait_draft'
    WHEN a.state='completed' AND x.status IN ('blocked','withdrawn','awaiting_publication') THEN 'hold'
    WHEN a.state='completed' AND q.kind='publish_correction' AND o.snapshot->>'head' IS DISTINCT FROM a.candidate_sha THEN CASE
        WHEN o.snapshot->>'state' IS DISTINCT FROM 'OPEN' OR o.snapshot->>'head' IS DISTINCT FROM q.subject->>'previousHead'
            OR o.snapshot->>'target' IS DISTINCT FROM q.subject->>'target'
            OR o.snapshot->>'base' IS DISTINCT FROM q.subject->>'baseSha' THEN 'inspect_revision'
        WHEN NOT coalesce(live_decision.active,false) OR q.answer='hold' THEN 'hold'
        WHEN q.answer='approve' THEN 'publish_correction'
        ELSE 'approve_correction' END
    WHEN a.state='completed' AND (x.status='created' OR q.kind='publish_correction') THEN CASE
        WHEN o.number IS NULL THEN 'wait_inspection'
        WHEN o.snapshot->>'head' IS DISTINCT FROM a.candidate_sha THEN 'inspect_revision'
        WHEN o.snapshot->>'state'='MERGED' THEN CASE
            WHEN monitor.evidence IS NOT NULL THEN 'wait_deployment'
            WHEN EXISTS(SELECT 1 FROM pull_handovers h
                WHERE (h.workspace_id,h.project_id,h.pull_number)=(o.workspace_id,o.project_id,o.number)
                    AND h.merged_sha=o.snapshot->>'mergedSha') THEN 'finished'
            ELSE 'deployment_handover' END
        WHEN o.snapshot->>'state'='CLOSED' THEN 'terminal'
        WHEN o.snapshot->>'checks' IN ('PENDING','UNKNOWN') THEN 'wait_ci'
        ELSE 'prepare_signoff' END
    WHEN a.state='completed' THEN 'approve_draft'
    ELSE 'inspect_execution'
END AS kind) decision;
