-- Freeze cost coverage at verification time, including unpriced or unreported turns.
ALTER TABLE pull_handovers ADD COLUMN worker_cost jsonb;
CREATE FUNCTION ticket_worker_cost(ws text, project text, ticket bigint) RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_strip_nulls(jsonb_build_object(
        'admittedTurns',count(*),'meteredTurns',count(t.model),'pricedTurns',count(t.estimated_usd),
        'inputTokens',coalesce(sum(t.input_tokens),0),'outputTokens',coalesce(sum(t.output_tokens),0),
        'estimatedUSD',sum(t.estimated_usd)))
    FROM (
        SELECT t.* FROM priced_model_turns t JOIN attempt_reports a
          ON (a.workspace_id,a.project_id,a.device_id,a.id)=(t.workspace_id,t.project_id,t.device_id,t.subject_id)
        WHERE t.kind='admission.turn_start' AND a.workspace_id=ws AND a.project_id=project AND a.issue_number=ticket
        UNION ALL
        SELECT t.* FROM priced_model_turns t JOIN worker_starts s
          ON (s.workspace_id,s.device_id,s.id)=(t.workspace_id,t.device_id,t.subject_id)
        WHERE t.kind='local_model.started' AND (t.project_id IS NULL OR t.project_id=project)
          AND s.workspace_id=ws AND s.project_id=project AND s.issue_number=ticket
    ) t
$$;
