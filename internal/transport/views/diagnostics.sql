-- Each queue retains its own ordered 1001-row sentinel. Empty queues have no age.
WITH queue_rows AS (
    SELECT 0 AS ordinal,'incident_intake' AS queue,count(*) AS count,min(since) AS oldest
    FROM (SELECT accepted_at AS since FROM incident_inbox WHERE workspace_id=$1 AND NOT processed ORDER BY since LIMIT 1001) bounded
    UNION ALL
    SELECT 1 AS ordinal,'incident_issues' AS queue,count(*) AS count,min(since) AS oldest
    FROM (SELECT approved_at AS since FROM incident_issues WHERE workspace_id=$1 AND status IN ('queued','reconciling') ORDER BY since LIMIT 1001) bounded
    UNION ALL
    SELECT 2 AS ordinal,'planned_issues' AS queue,count(*) AS count,min(since) AS oldest
    FROM (SELECT created_at AS since FROM planned_issues WHERE workspace_id=$1 AND status IN ('queued','reconciling') ORDER BY since LIMIT 1001) bounded
    UNION ALL
    SELECT 3 AS ordinal,'draft_prs' AS queue,count(*) AS count,min(since) AS oldest
    FROM (SELECT created_at AS since FROM draft_pr_intents WHERE workspace_id=$1 AND status IN ('queued','reconciling') ORDER BY since LIMIT 1001) bounded
    UNION ALL
    SELECT 4 AS ordinal,'attempt_controls' AS queue,count(*) AS count,min(since) AS oldest
    FROM (SELECT created_at AS since FROM controls WHERE workspace_id=$1 AND status IN ('requested','received') AND expires_at>now() ORDER BY since LIMIT 1001) bounded
), queues AS (
    SELECT json_agg(json_build_object('queue',queue,'pending',least(count,1000),'more',count>1000,
        'oldestPendingMs',CASE WHEN oldest IS NOT NULL THEN greatest(0,extract(epoch FROM (now()-oldest))*1000)::bigint END
    ) ORDER BY ordinal) AS rows FROM queue_rows
), observations AS (
WITH recent AS MATERIALIZED (
 SELECT state,reason_code,evidence FROM attempt_reports WHERE workspace_id=$1 ORDER BY updated_at DESC,id LIMIT 200
), groups AS (
 SELECT state,nullif(reason_code,'') AS reason,count(*) AS count FROM recent
 WHERE state='failed' OR reason_code<>'' GROUP BY state,reason_code
), stage_rows AS (
 SELECT item->>'phase' AS phase,(item->>'durationMs')::bigint AS ms
 FROM recent CROSS JOIN LATERAL jsonb_array_elements(evidence) AS item
), stages AS (
 SELECT phase,count(*) AS observations,count(ms) AS measured,round(avg(ms))::bigint AS "averageMs",max(ms) AS "maximumMs" FROM stage_rows GROUP BY phase
), last_permits AS (
 SELECT role,(extract(epoch FROM (ended_at-created_at))*1000)::bigint AS ms
 FROM general_model_turns WHERE workspace_id=$1 AND ended_at IS NOT NULL ORDER BY ended_at DESC LIMIT 200
), permits AS (
 SELECT role,count(*) AS measured,round(avg(ms))::bigint AS "averageMs",max(ms) AS "maximumMs" FROM last_permits WHERE ms>=0 GROUP BY role
), rejoins AS (
 SELECT created_at FROM events WHERE workspace_id=$1 AND kind='device.reconnected' AND created_at>=now()-interval '24 hours' ORDER BY created_at DESC LIMIT 1001
), contact AS (
 SELECT min(diagnostics_since) AS since,count(diagnostics_since) AS tracked,count(*) AS total FROM devices WHERE workspace_id=$1
)
SELECT jsonb_build_object(
 'sampledAttempts',(SELECT count(*) FROM recent),
 'attemptReasons',coalesce((SELECT jsonb_agg(groups ORDER BY state,reason) FROM groups),'[]'),
 'stageActivity',coalesce((SELECT jsonb_agg(stages ORDER BY phase) FROM stages),'[]'),
 'generalPermitHold',coalesce((SELECT jsonb_agg(permits ORDER BY role) FROM permits),'[]'),
 'contact',jsonb_build_object('trackingSince',since,'trackedDevices',tracked,'totalDevices',total,
   'observedReconnects24h',CASE WHEN tracked>0 THEN least((SELECT count(*) FROM rejoins),1000) END,
   'more',(SELECT count(*)>1000 FROM rejoins),'lastObservedReconnect24h',(SELECT max(created_at) FROM rejoins)))
AS samples FROM contact
)
SELECT to_json(now()) AS "observedAt",queues.rows AS queues,observations.samples AS observations
FROM queues,observations
