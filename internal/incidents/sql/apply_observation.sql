-- Called under the transaction that accepted a manual observation or claimed
-- an inbox row. Counting and setting processed cannot be committed separately.
WITH observation AS (
    UPDATE incident_inbox SET processed=true
    WHERE id=$1::bigint AND NOT processed
    RETURNING workspace_id,project_id,source,group_key,observed,expected,steps,seen_at
)
INSERT INTO incidents(id,workspace_id,project_id,source,group_key,observed,expected,steps,first_seen,last_seen,occurrences)
SELECT replace(gen_random_uuid()::text,'-',''),workspace_id,project_id,source,group_key,observed,expected,steps,seen_at,seen_at,1
FROM observation
ON CONFLICT(workspace_id,project_id,source,group_key) DO UPDATE SET
    first_seen=least(incidents.first_seen,excluded.first_seen),
    last_seen=greatest(incidents.last_seen,excluded.last_seen),
    observed=CASE WHEN incidents.redacted_at IS NOT NULL AND excluded.last_seen>=incidents.last_seen THEN excluded.observed ELSE incidents.observed END,
    expected=CASE WHEN incidents.redacted_at IS NOT NULL AND excluded.last_seen>=incidents.last_seen THEN excluded.expected ELSE incidents.expected END,
    steps=CASE WHEN incidents.redacted_at IS NOT NULL AND excluded.last_seen>=incidents.last_seen THEN excluded.steps ELSE incidents.steps END,
    redacted_at=CASE WHEN excluded.last_seen>=incidents.last_seen THEN NULL ELSE incidents.redacted_at END,
    occurrences=incidents.occurrences+1,version=incidents.version+1
RETURNING id,workspace_id,project_id
