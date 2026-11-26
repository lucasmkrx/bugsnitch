-- A redeployment may reuse a build ID. Each verified deployment has its own receipt.
ALTER TABLE pull_handovers ADD COLUMN monitor_deployments jsonb NOT NULL DEFAULT '{}',
    DROP CONSTRAINT pull_handovers_pkey,
    ADD PRIMARY KEY(workspace_id,project_id,pull_number,environment,build,monitor_deployments);

ALTER TABLE ticket_decisions DROP CONSTRAINT ticket_decisions_kind_check,
    DROP CONSTRAINT ticket_decisions_answer_check,
    ADD COLUMN feedback text,
    ADD CHECK(kind IN ('implementation_plan','publish_correction','owner_question')),
    ADD CHECK(CASE WHEN kind='owner_question' THEN
        (answer IS NULL AND feedback IS NULL OR answer IS NOT NULL AND answer='answered' AND feedback IS NOT NULL
         AND octet_length(feedback) BETWEEN 1 AND 2048)
        ELSE feedback IS NULL AND (answer IS NULL OR answer IN ('approve','hold')) END);

-- Keep dashboard and device on the same live checkpoint. Only the owning device
-- retrieves the answer text. Audit events and the dashboard carry its state.
ALTER VIEW attempt_progression RENAME TO attempt_progression_core;
CREATE VIEW attempt_progression AS
SELECT x.workspace_id,x.project_id,x.device_id,x.id,x.version,
    x.action || CASE
        WHEN x.action->>'kind'='wait_deployment' AND EXISTS (
            SELECT FROM pull_handovers h,jsonb_to_recordset(x.action->'deployment')
                AS d("monitorId" text,"deploymentKey" text,environment text,build text,held boolean)
            WHERE (h.workspace_id,h.project_id,h.pull_number,h.merged_sha,h.target)=
                (x.workspace_id,x.project_id,(x.action->'pull'->>'number')::bigint,x.action->'pull'->>'mergedSha',x.action->'pull'->>'target')
              AND h.environment=d.environment AND h.build=d.build AND NOT d.held
              AND h.monitor_deployments->>d."monitorId"=d."deploymentKey"
        ) THEN jsonb_build_object('kind','finished','disposition','finish','reason','Target deployment verified and handed over.')
        WHEN x.action->>'kind'='owner_input' AND q.kind='owner_question' THEN
            jsonb_strip_nulls(jsonb_build_object('decisionKind',q.kind,'question',q.subject->>'question',
                'approver',q.recipient,'expiresAt',q.expires_at,'answer',q.answer))
        WHEN x.action->>'kind'='review_plan' AND q.kind='implementation_plan' AND q.subject ? 'planSummary'
            THEN jsonb_build_object('planSummary',q.subject->>'planSummary')
        ELSE '{}'::jsonb END AS action
FROM attempt_progression_core x
LEFT JOIN ticket_decisions q ON (q.workspace_id,q.attempt_id,q.attempt_version)=(x.workspace_id,x.id,x.version)
    AND ((q.kind='owner_question' AND x.action->>'kind'='owner_input') OR
         (q.kind='implementation_plan' AND x.action->>'kind'='review_plan'))
    AND q.expires_at>now()
    AND EXISTS(SELECT 1 FROM projects p WHERE (p.workspace_id,p.id,p.policy_version)=(x.workspace_id,x.project_id,q.subject_policy))
    AND q.plan_id=(SELECT v.id FROM plan_versions v WHERE (v.workspace_id,v.project_id)=(x.workspace_id,x.project_id)
        AND v.approved_at IS NOT NULL ORDER BY v.version DESC LIMIT 1);
