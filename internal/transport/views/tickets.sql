SELECT all_tickets.*, CASE WHEN l.linear_id IS NOT NULL THEN json_build_object(
    'id',l.linear_id,'identifier',l.identifier,'title',l.title,'url',l.url,'state',l.state_name,'delegated',EXISTS (SELECT FROM linear_sessions ls WHERE (ls.workspace_id,ls.project_id,ls.issue_number)=(l.workspace_id,l.project_id,l.issue_number) AND NOT ls.finished),'checkedAt',l.checked_at) END AS linear
FROM (
	SELECT x.incident_id AS id,x.project_id AS "projectId",x.number,p.repository,i.observed AS title,x.github_state AS state,'incident' AS source,
	  x.reason, x.checked_at AS "checkedAt" FROM incident_issues x JOIN incidents i ON i.id=x.incident_id JOIN projects p ON p.id=x.project_id WHERE x.workspace_id=$1 AND x.number IS NOT NULL
	UNION ALL
	SELECT 'github-'||z.repository_id||'-'||z.github_id,z.project_id,z.number,p.repository,coalesce(nullif(z.title,''),'Issue #'||z.number),
	  CASE WHEN NOT m.active OR z.availability<>'current' OR z.checked_at IS NULL OR z.checked_at<=now()-interval '20 minutes' THEN 'unknown' ELSE z.source_state END,'plan',
	  CASE WHEN NOT m.active THEN 'Repository mapping disabled' WHEN z.availability<>'current' THEN z.reason
	       WHEN z.checked_at<=now()-interval '20 minutes' THEN 'GitHub state needs a fresh read'
	       WHEN NOT EXISTS(SELECT FROM plan_issue_bindings b JOIN plan_versions v ON v.id=b.plan_id WHERE (b.workspace_id,b.project_id,b.number,b.generation)=(z.workspace_id,z.project_id,z.number,z.generation) AND v.id=(SELECT id FROM plan_versions WHERE workspace_id=z.workspace_id AND project_id=z.project_id AND approved_at IS NOT NULL ORDER BY version DESC LIMIT 1)) THEN 'A fresh reviewed plan is required' ELSE '' END,z.checked_at
	FROM planned_issue_state z JOIN projects p ON (p.workspace_id,p.id)=(z.workspace_id,z.project_id) JOIN github_mappings m ON (m.workspace_id,m.project_id,m.repository_id,m.installation_id)=(z.workspace_id,z.project_id,z.repository_id,z.installation_id) WHERE z.workspace_id=$1) all_tickets
LEFT JOIN linear_issue_links l ON l.workspace_id=$1 AND (l.project_id,l.issue_number)=(all_tickets."projectId",all_tickets.number)
ORDER BY "checkedAt" DESC NULLS LAST,id LIMIT 500
