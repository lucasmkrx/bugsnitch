-- Limit plan versions before expanding tickets. Resolve progress and projection from
-- one MVCC snapshot. A plan's authored target and forecast are immutable.
WITH recent_plans AS (
    SELECT v.*, w.stopped OR p.paused AS paused,
        NOT EXISTS(SELECT FROM plan_versions newer WHERE
            (newer.workspace_id,newer.project_id)=(v.workspace_id,v.project_id)
            AND newer.approved_at IS NOT NULL AND newer.version>v.version) AS current_plan
    FROM plan_versions v JOIN projects p ON (p.workspace_id,p.id)=(v.workspace_id,v.project_id)
    JOIN workspaces w ON w.id=v.workspace_id WHERE v.workspace_id=$1
    ORDER BY v.created_at DESC,v.id LIMIT 150
)
SELECT v.id, v.project_id AS "projectId", v.version, coalesce(v.document->>'title','') AS title,
       -- Both plan writers persist the typed, validated document. Reuse it on
       -- the normal path. Reconstruct legacy partial records with the original
       -- field defaults and allowlist before exposing them.
       CASE WHEN jsonb_typeof(v.document)<>'object' OR malformed_ticket
              OR EXISTS (SELECT FROM jsonb_each(v.document) f
                         WHERE f.key IN ('title','milestone','assumptions','target','forecast')
                           AND jsonb_typeof(f.value) NOT IN ('string','null'))
         THEN NULL
         WHEN v.document ?& ARRAY['title','milestone','assumptions','tickets']
              AND jsonb_typeof(v.document->'title')='string' AND jsonb_typeof(v.document->'milestone')='string'
              AND jsonb_typeof(v.document->'assumptions')='string'
              AND v.document->'target' IS DISTINCT FROM 'null'::jsonb AND v.document->'forecast' IS DISTINCT FROM 'null'::jsonb
              AND v.document-ARRAY['title','milestone','assumptions','tickets','target','forecast']='{}'
              AND v.document->>'target' IS DISTINCT FROM '' AND v.document->>'forecast' IS DISTINCT FROM ''
              AND coalesce(canonical_tickets,true)
       THEN v.document ELSE
         jsonb_strip_nulls(jsonb_build_object('title',coalesce(v.document->>'title',''),
           'milestone',coalesce(v.document->>'milestone',''),'assumptions',coalesce(v.document->>'assumptions',''),
           'target',nullif(v.document->>'target',''),'forecast',nullif(v.document->>'forecast','')))
         || jsonb_build_object('tickets',CASE WHEN jsonb_typeof(v.document->'tickets')='array' THEN (
           SELECT coalesce(jsonb_agg(jsonb_build_object('number',coalesce(t.number,0),'title',coalesce(t.title,''),
             'criteria',t.criteria,'verification',coalesce(t.verification,''),'priority',coalesce(t.priority,0),
             'estimateHours',coalesce(t."estimateHours",0),'dependencies',t.dependencies,'resources',t.resources) ORDER BY i.ord),'[]')
           FROM jsonb_array_elements(v.document->'tickets') WITH ORDINALITY i(doc,ord)
           CROSS JOIN LATERAL jsonb_to_record(i.doc) t(number bigint,title text,criteria jsonb,verification text,
             priority int,"estimateHours" int,dependencies jsonb,resources jsonb)
         ) END) END AS document,
       v.execution_order AS "executionOrder", v.created_at AS "createdAt",
       CASE WHEN v.approved_at IS NULL THEN 'proposed' ELSE 'approved' END AS status,
       jsonb_strip_nulls(jsonb_build_object(
         'status',CASE WHEN reason IS NOT NULL THEN 'unknown' WHEN completed=total THEN 'complete' ELSE 'projected' END,
         'asOf',current_date,'completedTickets',completed,'totalTickets',total,
         'completedEstimateHours',done_hours,'totalEstimateHours',total_hours,'reason',reason,'date',finish,
         'targetDeltaDays',CASE WHEN finish IS NOT NULL AND v.document->>'target' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
            AND pg_input_is_valid(v.document->>'target','date') THEN finish-(v.document->>'target')::date END
       )) AS "progressForecast"
FROM recent_plans v CROSS JOIN LATERAL (
    SELECT count(*) AS total, count(*) FILTER (WHERE valid) AS completed,
           coalesce(sum(hours),0) AS total_hours, coalesce(sum(hours) FILTER (WHERE valid),0) AS done_hours,
           count(DISTINCT e.created_at::date) FILTER (WHERE valid) AS days_observed,
           max(e.created_at::date) FILTER (WHERE valid) AS last_day, bool_or(NOT fresh) AS stale,
           bool_or(jsonb_typeof(item.doc)<>'object' OR NOT typed_fields) AS malformed_ticket,
           bool_and(item.doc ?& keys AND item.doc-keys='{}' AND item.doc->'title'<>'null' AND item.doc->'verification'<>'null'
              AND item.doc->'number'<>'null' AND item.doc->'estimateHours'<>'null' AND item.doc->'priority'<>'null') AS canonical_tickets
    FROM jsonb_array_elements(nullif(v.document->'tickets','null')) item(doc)
    CROSS JOIN LATERAL (SELECT (doc->>'number')::bigint AS number,(doc->>'estimateHours')::int AS "estimateHours",
       ARRAY['number','title','criteria','verification','priority','estimateHours','dependencies','resources'] AS keys) t
    -- Read only known scalar and list types. In particular PostgreSQL's ->>
    -- and jsonb_to_record could otherwise stringify arbitrary nested objects.
    CROSS JOIN LATERAL (SELECT
        coalesce(jsonb_typeof(doc->'number'),'null') IN ('number','null')
        AND coalesce(jsonb_typeof(doc->'estimateHours'),'null') IN ('number','null')
        AND coalesce(jsonb_typeof(doc->'priority'),'null') IN ('number','null')
        AND coalesce(jsonb_typeof(doc->'title'),'null') IN ('string','null')
        AND coalesce(jsonb_typeof(doc->'verification'),'null') IN ('string','null')
        AND coalesce(jsonb_typeof(doc->'criteria'),'null') IN ('array','null')
        AND coalesce(jsonb_typeof(doc->'resources'),'null') IN ('array','null')
        AND coalesce(jsonb_typeof(doc->'dependencies'),'null') IN ('array','null')
        AND NOT coalesce(nullif(doc->'criteria','null') @? '$[*] ? (@.type() != "string")',false)
        AND NOT coalesce(nullif(doc->'resources','null') @? '$[*] ? (@.type() != "string")',false)
        AND NOT coalesce(nullif(doc->'dependencies','null') @? '$[*] ? (@.type() != "number")',false) AS typed_fields
    ) shape
    LEFT JOIN prerequisite_evidence e ON (e.workspace_id,e.project_id,e.plan_id,e.issue_number)=(v.workspace_id,v.project_id,v.id,t.number)
    LEFT JOIN planned_issue_state z ON (z.workspace_id,z.project_id,z.number)=(v.workspace_id,v.project_id,t.number)
    LEFT JOIN github_mappings m ON (m.workspace_id,m.project_id,m.installation_id,m.repository_id)=(z.workspace_id,z.project_id,z.installation_id,z.repository_id)
    LEFT JOIN plan_issue_bindings b ON (b.workspace_id,b.project_id,b.plan_id,b.number)=(v.workspace_id,v.project_id,v.id,t.number)
    CROSS JOIN LATERAL (SELECT z.number IS NULL OR coalesce(m.active AND z.availability='current' AND z.source_state IN ('open','closed')
        AND z.checked_at>now()-interval '20 minutes' AND b.generation=z.generation,false) AS fresh) binding
    CROSS JOIN LATERAL (SELECT fresh AND e.created_at BETWEEN v.approved_at AND now() AS valid, coalesce(t."estimateHours",0) AS hours) evidence
) progress
CROSS JOIN LATERAL (SELECT ceil((total_hours-done_hours)*(current_date-v.approved_at::date+1)::numeric/nullif(done_hours,0)) AS remaining_days) pace
CROSS JOIN LATERAL (SELECT CASE
    WHEN v.approved_at IS NULL THEN 'Approval is needed before recording delivery progress.'
    WHEN NOT v.current_plan THEN 'A newer approved plan replaced this scope.'
    WHEN stale THEN 'A canonical issue revision needs a current read or a fresh plan binding.'
    WHEN total=0 THEN 'The plan has no estimated tickets.'
    WHEN completed=total THEN NULL
    WHEN v.paused THEN 'Dispatch is paused. The resumption date is unknown.'
    WHEN days_observed<2 OR done_hours<=0 THEN 'Record integration evidence on at least two different UTC dates to estimate the remaining work.'
    WHEN remaining_days>date '9999-12-31'-current_date THEN 'The projection exceeds the supported calendar range.'
END AS reason) outcome
CROSS JOIN LATERAL (SELECT CASE WHEN reason IS NULL THEN
    CASE WHEN completed=total THEN last_day ELSE current_date+remaining_days::int END END AS finish) projection
ORDER BY v.created_at DESC,v.id
