SELECT CASE WHEN NOT coalesce(f.malformed,false) THEN json_object(
       'id' VALUE i.id, 'projectId' VALUE i.project_id,
       'title' VALUE CASE WHEN length(title)>110 THEN left(title,109)||'…' ELSE title END,
       'observed' VALUE i.observed, 'expected' VALUE i.expected, 'steps' VALUE i.steps, 'source' VALUE i.source,
       'status' VALUE i.state, 'occurrences' VALUE i.occurrences, 'firstSeen' VALUE i.first_seen,
       'lastSeen' VALUE i.last_seen, 'version' VALUE i.version, 'issueNumber' VALUE x.number,
       'issueState' VALUE x.github_state, 'promotion' VALUE x.status, 'promotionReason' VALUE nullif(x.reason,''),
       'providerGroup' VALUE CASE WHEN i.source='sentry' THEN regexp_replace(i.group_key,'^.*/','') END,
       'environment' VALUE nullif(latest.environment,''), 'release' VALUE nullif(latest.release,''),
       'frames' VALUE f.frames ABSENT ON NULL) END AS document
FROM incidents i
CROSS JOIN LATERAL (SELECT split_part(i.observed,chr(10),1) AS title) names
LEFT JOIN incident_issues x ON (i.workspace_id,i.project_id,i.id)=(x.workspace_id,x.project_id,x.incident_id)
LEFT JOIN LATERAL (
    SELECT environment,release,frames FROM incident_inbox b
    WHERE (b.workspace_id,b.project_id,b.source,b.group_key)=(i.workspace_id,i.project_id,i.source,i.group_key)
    ORDER BY b.seen_at DESC,b.id DESC LIMIT 1
) latest ON true
LEFT JOIN LATERAL (
    SELECT json_agg(json_build_object('file',coalesce(frame->>'file',''),'line',coalesce((frame->>'line')::int,0))) AS frames,
           bool_or(jsonb_typeof(frame) NOT IN ('object','null')
               OR coalesce(jsonb_typeof(frame->'file'),'null') NOT IN ('string','null')
               OR coalesce(jsonb_typeof(frame->'line'),'null') NOT IN ('number','null')) AS malformed
    FROM jsonb_array_elements(nullif(latest.frames,'null')) AS frame
) f ON true
WHERE i.workspace_id=$1 ORDER BY i.last_seen DESC,i.id LIMIT 500
