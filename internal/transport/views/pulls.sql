-- The dashboard and post-refresh response read this same evidence and ordered list of blockers.
-- Callers choose either the newest 500 rows or the requested PR.
SELECT CASE WHEN
    NOT EXISTS (SELECT FROM jsonb_each(o.snapshot) f WHERE jsonb_typeof(f.value)<>'null' AND CASE
        WHEN f.key IN ('mergedSha','title','state','issueState','head','base','target','criteria','checks','review') THEN jsonb_typeof(f.value)<>'string'
        WHEN f.key='checkCount' THEN jsonb_typeof(f.value)<>'number'
        WHEN f.key='draft' THEN jsonb_typeof(f.value)<>'boolean'
        WHEN f.key='queue' THEN jsonb_typeof(f.value)<>'object'
        ELSE false END)
    AND NOT EXISTS (SELECT FROM jsonb_each(s.queue) f WHERE jsonb_typeof(f.value)<>'null' AND CASE
        WHEN f.key IN ('ownerType','entryState','entryHead','entryBase') THEN jsonb_typeof(f.value)<>'string'
        WHEN f.key='present' THEN jsonb_typeof(f.value)<>'boolean'
        WHEN f.key='position' THEN jsonb_typeof(f.value)<>'number'
        WHEN f.key='rules' THEN jsonb_typeof(f.value)<>'object'
        ELSE false END)
    AND NOT EXISTS (SELECT FROM jsonb_each(rules) f WHERE jsonb_typeof(f.value)<>'null' AND CASE
        WHEN f.key='digest' THEN jsonb_typeof(f.value)<>'string'
        WHEN f.key='queueRequired' THEN jsonb_typeof(f.value)<>'boolean'
        WHEN f.key IN ('requiredChecks','appBoundChecks') THEN jsonb_typeof(f.value)<>'number'
        ELSE false END)
THEN json_object(
       'projectId' VALUE o.project_id, 'number' VALUE o.number, 'issueNumber' VALUE o.issue_number, 'version' VALUE o.version,
       'policyVersion' VALUE o.policy_version, 'planVersion' VALUE o.plan_version, 'observedAt' VALUE o.observed_at,
       'title' VALUE coalesce(s.title,''), 'state' VALUE coalesce(s.state,''), 'issueState' VALUE coalesce(s."issueState",''),
       'head' VALUE coalesce(s.head,''), 'base' VALUE coalesce(s.base,''), 'target' VALUE coalesce(s.target,''),
       'criteria' VALUE coalesce(s.criteria,''), 'checks' VALUE coalesce(s.checks,''), 'checkCount' VALUE coalesce(s."checkCount",0),
       'review' VALUE coalesce(s.review,''), 'draft' VALUE coalesce(s.draft,false),
       'watching' VALUE o.watching AND s.state='OPEN',
       'signOff' VALUE CASE WHEN approved.reference IS NOT NULL AND coalesce(m.active,false) AND NOT p.paused AND NOT w.stopped
            AND o.policy_version=p.policy_version AND o.plan_version=current_plan.version AND o.observed_at>=now()-interval '10 minutes'
            THEN json_build_object('at',approved.recorded_at) END,
       'mergedSha' VALUE nullif(s."mergedSha",''), 'validation' VALUE nullif(val.reference,''), 'independentReview' VALUE nullif(rev.reference,''),
       'handovers' VALUE CASE WHEN s.state='MERGED' AND nullif(s."mergedSha",'') IS NOT NULL THEN (
           SELECT json_agg(json_build_object('version',h.inspection_version,'environment',h.environment,'build',h.build,
              'mergedSha',h.merged_sha,'target',h.target,'evidenceDigest',h.evidence_digest,
              'summary',h.summary,'testSteps',h.test_steps,'recordedAt',h.recorded_at,'cost',h.worker_cost,
              'recordedBy',coalesce((SELECT name FROM workspace_members WHERE (workspace_id,id)=(h.workspace_id,h.actor_id)), 'Owner'))
              ORDER BY h.recorded_at DESC,h.environment,h.build)
           FROM (SELECT * FROM pull_handovers WHERE (workspace_id,project_id,pull_number)=(o.workspace_id,o.project_id,o.number)
                 AND (merged_sha,target)=(s."mergedSha",s.target) ORDER BY recorded_at DESC,environment,build LIMIT 5) h
       ) END,
       'queue' VALUE CASE WHEN s.queue IS NOT NULL THEN json_object(
         'ownerType' VALUE coalesce(s.queue->>'ownerType',''), 'present' VALUE coalesce((s.queue->>'present')::bool,false),
         'entryState' VALUE nullif(s.queue->>'entryState',''), 'position' VALUE (s.queue->>'position')::int,
         'entryHead' VALUE nullif(s.queue->>'entryHead',''), 'entryBase' VALUE nullif(s.queue->>'entryBase',''),
         'rules' VALUE CASE WHEN rules IS NOT NULL THEN json_object(
           'digest' VALUE coalesce(rules->>'digest',''), 'queueRequired' VALUE coalesce((rules->>'queueRequired')::bool,false),
           'requiredChecks' VALUE coalesce((rules->>'requiredChecks')::int,0), 'appBoundChecks' VALUE coalesce((rules->>'appBoundChecks')::int,0)
         ) END ABSENT ON NULL
       ) END,
       'queueDelivery' VALUE CASE WHEN qa.version IS NOT NULL THEN json_build_object('state',qa.state,'reason',qa.reason,'groupSha',qa.group_sha,'version',qa.version,
          'target',qa.target,'base',qa.base,'head',qa.head,'environment',qa.environment,'expiresAt',qa.expires_at) END,
       'autoMerge' VALUE coalesce(qa.state='passed' AND s.state='OPEN' AND (qa.head,qa.base,qa.target)=(s.head,s.base,s.target) AND s.queue->>'entryState' IS NOT NULL,false), 'blockers' VALUE
       array_remove(ARRAY[
         CASE WHEN NOT coalesce(m.active,false) THEN 'Repository authorization unavailable' END,
         CASE WHEN p.paused THEN 'Project dispatch paused' END,
         CASE WHEN w.stopped THEN 'Workspace dispatch stopped' END,
         CASE WHEN o.policy_version<>p.policy_version OR o.plan_version<>current_plan.version THEN 'Project or approved plan changed since inspection' END,
         CASE WHEN o.observed_at<now()-interval '10 minutes' THEN 'Inspection older than ten minutes' END,
         CASE WHEN s.state IS DISTINCT FROM 'OPEN' OR s.draft THEN 'Pull request is closed, merged or a draft' END,
         CASE WHEN s."issueState" IS DISTINCT FROM 'OPEN' THEN 'Canonical issue is closed' END,
         CASE WHEN coalesce(s.base,'')='' THEN 'Current target branch revision unavailable' END,
         CASE WHEN coalesce(s."checkCount",0)=0 OR s.checks IS DISTINCT FROM 'SUCCESS' THEN 'Successful head checks are missing' END,
         CASE WHEN s.review IS DISTINCT FROM 'APPROVED' THEN 'GitHub review approval is missing' END,
         CASE WHEN coalesce(val.reference,'')='' THEN 'Combined validation evidence is missing' END,
         CASE WHEN coalesce(rev.reference,'')='' THEN 'Independent review evidence is missing' END,
         CASE WHEN approved.reference IS NULL THEN 'Integration sign-off for this inspected revision is missing' END,
         CASE WHEN s.queue->>'ownerType'='User' THEN 'GitHub merge queues require an eligible organization-owned repository' END,
         CASE WHEN s.queue IS NOT NULL AND NOT coalesce((s.queue->>'present')::bool,false) THEN 'GitHub returned no merge queue for this target' END,
         CASE WHEN rules IS NOT NULL AND NOT coalesce((rules->>'queueRequired')::bool,false) THEN 'The active target rulesets do not require a merge queue' END,
         CASE WHEN rules IS NOT NULL AND coalesce((rules->>'requiredChecks')::int,0)=0 THEN 'The active target rulesets have no required status checks' END,
         CASE WHEN coalesce((rules->>'appBoundChecks')::int,0)<coalesce((rules->>'requiredChecks')::int,0) THEN 'Some required checks accept an unspecified producer' END,
         CASE WHEN s.queue IS NOT NULL AND rules IS NULL THEN 'Active target rules could not be verified' END,
         CASE WHEN qa.state IS NULL OR qa.state IN ('revoked','held') THEN 'Protected delivery requires explicit authorization for the approved target and its deployment effects.' END
       ],NULL) ABSENT ON NULL) END AS document
FROM pull_observations o
JOIN projects p ON (o.workspace_id,o.project_id)=(p.workspace_id,p.id)
JOIN workspaces w ON w.id=o.workspace_id
CROSS JOIN LATERAL (SELECT coalesce(max(version),0) AS version FROM plan_versions v
 WHERE (v.workspace_id,v.project_id)=(p.workspace_id,p.id) AND approved_at IS NOT NULL) current_plan
LEFT JOIN github_mappings m ON (o.workspace_id,o.project_id)=(m.workspace_id,m.project_id)
CROSS JOIN LATERAL jsonb_to_record(o.snapshot) AS s(title text,state text,"issueState" text,head text,base text,
    target text,criteria text,checks text,"checkCount" int,review text,draft bool,"mergedSha" text,queue jsonb)
CROSS JOIN LATERAL (SELECT nullif(s.queue->'rules','null') AS rules) queue_rules
LEFT JOIN LATERAL (SELECT * FROM pull_queue_permits a WHERE (a.workspace_id,a.project_id,a.number)=(o.workspace_id,o.project_id,o.number) ORDER BY a.version DESC LIMIT 1) qa ON true
LEFT JOIN pull_evidence val ON (val.workspace_id,val.project_id,val.number,val.version)=(o.workspace_id,o.project_id,o.number,o.version) AND val.kind='combined_validation'
LEFT JOIN pull_evidence rev ON (rev.workspace_id,rev.project_id,rev.number,rev.version)=(o.workspace_id,o.project_id,o.number,o.version) AND rev.kind='independent_review'
LEFT JOIN pull_evidence approved ON (approved.workspace_id,approved.project_id,approved.number,approved.version)=(o.workspace_id,o.project_id,o.number,o.version) AND approved.kind='sign_off'
WHERE o.workspace_id=$1
