-- Empty scope denotes the entire workspace. Project scopes must belong to that workspace.
CREATE TABLE monthly_cost_policies (
    workspace_id text NOT NULL REFERENCES workspaces, scope text NOT NULL,
    project_ref text GENERATED ALWAYS AS (nullif(scope,'')) STORED,
    version bigint NOT NULL CHECK(version>0), mode text NOT NULL CHECK(mode IN ('off','alert','stop')),
    threshold_usd numeric(18,6) CHECK(threshold_usd>0 AND threshold_usd<=1000000000),
    CHECK ((mode='off')=(threshold_usd IS NULL)),
    PRIMARY KEY(workspace_id,scope), FOREIGN KEY(workspace_id,project_ref) REFERENCES projects(workspace_id,id)
);

-- Immutable device measurements are charged to the admission month at its effective model price.
CREATE TABLE monthly_cost_usage (
    workspace_id text NOT NULL REFERENCES workspaces, scope text NOT NULL, month date NOT NULL,
    metered_turns bigint NOT NULL, priced_turns bigint NOT NULL, estimated_usd numeric NOT NULL,
    PRIMARY KEY(workspace_id,scope,month)
);
INSERT INTO monthly_cost_usage(workspace_id,scope,month,metered_turns,priced_turns,estimated_usd)
SELECT e.workspace_id,s.scope,date_trunc('month',e.created_at AT TIME ZONE 'UTC')::date,count(*),count(e.estimated_usd),coalesce(sum(e.estimated_usd),0)
FROM priced_model_turns e CROSS JOIN LATERAL (SELECT '' AS scope UNION ALL SELECT e.project_id WHERE e.project_id IS NOT NULL) s
WHERE e.model IS NOT NULL GROUP BY 1,2,3;

CREATE VIEW monthly_cost_status AS
SELECT p.workspace_id,p.scope,p.version,p.mode,p.threshold_usd,coalesce(u.estimated_usd,0)>=p.threshold_usd AS reached
FROM monthly_cost_policies p LEFT JOIN monthly_cost_usage u ON (u.workspace_id,u.scope)=(p.workspace_id,p.scope)
    AND u.month=date_trunc('month',now() AT TIME ZONE 'UTC')::date WHERE p.mode<>'off';

CREATE TABLE monthly_cost_notifications (
    workspace_id text NOT NULL, scope text NOT NULL, version bigint NOT NULL, month date NOT NULL,
    PRIMARY KEY(workspace_id,scope,version,month), FOREIGN KEY(workspace_id,scope) REFERENCES monthly_cost_policies(workspace_id,scope)
);
CREATE FUNCTION notify_monthly_cost_threshold(ws text,project text) RETURNS void LANGUAGE sql AS $$
    WITH added AS (
        INSERT INTO monthly_cost_notifications
        SELECT workspace_id,scope,version,date_trunc('month',now() AT TIME ZONE 'UTC')::date
        FROM monthly_cost_status WHERE workspace_id=ws AND scope IN ('',project) AND reached
        ON CONFLICT DO NOTHING RETURNING *
    )
    INSERT INTO events(workspace_id,project_id,kind,subject_id,actor_source,actor_id,reason,subject_version)
      SELECT workspace_id,nullif(scope,''),'budget.cost_threshold_reached',month::text,'service','model-metering',
          'Measured and priced model turns reached the monthly USD threshold',version FROM added
$$;

CREATE FUNCTION update_monthly_cost_usage() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    turn priced_model_turns%ROWTYPE;
    admission_month date;
BEGIN
    SELECT * INTO STRICT turn FROM priced_model_turns WHERE seq=NEW.event_seq;
    admission_month:=date_trunc('month',turn.created_at AT TIME ZONE 'UTC')::date;
    INSERT INTO monthly_cost_usage AS u(workspace_id,scope,month,metered_turns,priced_turns,estimated_usd)
    SELECT turn.workspace_id,s.scope,admission_month,1,(turn.estimated_usd IS NOT NULL)::integer,coalesce(turn.estimated_usd,0)
       FROM (SELECT '' AS scope UNION ALL SELECT turn.project_id WHERE turn.project_id IS NOT NULL) s
    ON CONFLICT(workspace_id,scope,month) DO UPDATE SET metered_turns=u.metered_turns+1,
        priced_turns=u.priced_turns+excluded.priced_turns,estimated_usd=u.estimated_usd+excluded.estimated_usd;
    IF admission_month=date_trunc('month',now() AT TIME ZONE 'UTC')::date THEN
        PERFORM notify_monthly_cost_threshold(turn.workspace_id,turn.project_id);
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER measurement_monthly_cost AFTER INSERT ON model_turn_metering
FOR EACH ROW EXECUTE FUNCTION update_monthly_cost_usage();
CREATE INDEX monthly_project_admissions ON events(workspace_id,project_id,created_at)
  WHERE kind IN ('admission.turn_start','local_model.started');
