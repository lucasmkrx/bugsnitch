CREATE TABLE release_plans(
 workspace_id text NOT NULL REFERENCES workspaces, id text NOT NULL, name text NOT NULL,
 environment text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), cancelled_at timestamptz,
 PRIMARY KEY(workspace_id,id)
);
CREATE TABLE release_stages(
 workspace_id text NOT NULL, release_id text NOT NULL, position integer NOT NULL CHECK(position>0),
 project_id text NOT NULL, pull_number bigint NOT NULL, head text NOT NULL, target text NOT NULL,
 PRIMARY KEY(workspace_id,release_id,position),
 UNIQUE(workspace_id,release_id,project_id,pull_number),
 FOREIGN KEY(workspace_id,release_id) REFERENCES release_plans(workspace_id,id),
 FOREIGN KEY(workspace_id,project_id,pull_number) REFERENCES pull_observations(workspace_id,project_id,number)
);
CREATE INDEX release_stage_pull ON release_stages(workspace_id,project_id,pull_number);
CREATE VIEW release_stage_status AS
SELECT s.*,r.environment,r.cancelled_at,
 CASE WHEN (s.head,s.target) IS DISTINCT FROM (o.snapshot->>'head',o.snapshot->>'target') THEN 'changed'
 WHEN o.snapshot->>'state'='CLOSED' THEN 'closed'
 WHEN EXISTS (SELECT FROM deployment_holds h JOIN deployment_monitors m ON m.id=h.monitor_id
   WHERE (m.workspace_id,m.project_id,m.environment)=(s.workspace_id,s.project_id,r.environment)
   AND h.cleared_at IS NULL) THEN 'deployment_held'
 WHEN o.snapshot->>'state'='MERGED' AND EXISTS(SELECT FROM pull_handovers h WHERE
 (h.workspace_id,h.project_id,h.pull_number,h.merged_sha,h.target,h.environment) =
 (s.workspace_id,s.project_id,s.pull_number,o.snapshot->>'mergedSha',s.target,r.environment)) THEN 'handed_over'
 WHEN o.snapshot->>'state'='MERGED' THEN 'awaiting_handover' ELSE 'pending_merge' END AS status
FROM release_stages s JOIN release_plans r ON (s.workspace_id,s.release_id)=(r.workspace_id,r.id)
JOIN pull_observations o ON (s.workspace_id,s.project_id,s.pull_number)=(o.workspace_id,o.project_id,o.number);
CREATE VIEW release_queue_holds AS
SELECT current.workspace_id,current.project_id,current.pull_number FROM release_stage_status current
WHERE current.cancelled_at IS NULL AND (current.status IN ('changed','closed','deployment_held') OR EXISTS (
 SELECT FROM release_stage_status prior WHERE prior.workspace_id=current.workspace_id AND prior.release_id=current.release_id
 AND prior.position<current.position AND prior.status<>'handed_over'));
