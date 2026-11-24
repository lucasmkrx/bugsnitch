ALTER TABLE workspaces ADD COLUMN monthly_turn_limit integer CHECK(monthly_turn_limit BETWEEN 0 AND 10000000);
CREATE TABLE monthly_model_usage (
 workspace_id text NOT NULL REFERENCES workspaces,
 month date NOT NULL,
 turns bigint NOT NULL CHECK(turns>=0),
 PRIMARY KEY(workspace_id,month)
);
INSERT INTO monthly_model_usage(workspace_id,month,turns)
 SELECT workspace_id,date_trunc('month',created_at AT TIME ZONE 'UTC')::date,count(*)
 FROM events WHERE kind IN ('admission.turn_start','local_model.started')
 AND created_at >= date_trunc('month',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
 GROUP BY 1,2;
