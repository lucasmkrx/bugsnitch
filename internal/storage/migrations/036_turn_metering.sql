-- The admission event pins the device, subject, sequence, project and start time.
CREATE TABLE model_turn_metering (
  event_seq bigint PRIMARY KEY REFERENCES events(seq) ON DELETE CASCADE,
  model text NOT NULL CHECK(length(model) BETWEEN 1 AND 120),
  input_tokens bigint NOT NULL CHECK(input_tokens BETWEEN 0 AND 4294967296),
  output_tokens bigint NOT NULL CHECK(output_tokens BETWEEN 0 AND 4294967296),
  received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX metering_admitted ON events(workspace_id,actor_id,subject_id,subject_version,kind)
  WHERE kind IN ('admission.turn_start','local_model.started');

-- Apply the price in force at admission time. A null price ends that model's rate.
CREATE TABLE model_price_history (
 workspace_id text NOT NULL REFERENCES workspaces, model text NOT NULL CHECK(length(model) BETWEEN 1 AND 120),
 version bigint NOT NULL CHECK(version>=1), effective_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 input_per_million_usd numeric(18,8) CHECK(input_per_million_usd BETWEEN 0 AND 100000),
 output_per_million_usd numeric(18,8) CHECK(output_per_million_usd BETWEEN 0 AND 100000),
 CHECK ((input_per_million_usd IS NULL) = (output_per_million_usd IS NULL)),
 PRIMARY KEY(workspace_id,model,version)
);
CREATE INDEX model_rate_at ON model_price_history(workspace_id,model,effective_at DESC,version DESC);

CREATE VIEW priced_model_turns AS
SELECT e.seq,e.workspace_id,e.project_id,e.actor_id AS device_id,e.subject_id,e.kind,e.created_at,
       m.model,m.input_tokens,m.output_tokens,r.version AS rate_version,
       (m.input_tokens*r.input_per_million_usd + m.output_tokens*r.output_per_million_usd)/1000000 AS estimated_usd
FROM events e LEFT JOIN model_turn_metering m ON m.event_seq=e.seq
LEFT JOIN LATERAL (
    SELECT version,input_per_million_usd,output_per_million_usd FROM model_price_history h
    WHERE h.workspace_id=e.workspace_id AND h.model=m.model AND h.effective_at<=e.created_at
    ORDER BY h.effective_at DESC,h.version DESC LIMIT 1
) r ON true
WHERE e.kind IN ('admission.turn_start','local_model.started');
