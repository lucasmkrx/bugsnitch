-- The first sync on an instrumented service establishes contact monitoring.
ALTER TABLE devices ADD COLUMN diagnostics_since timestamptz;
CREATE INDEX ops_incident_inbox ON incident_inbox(workspace_id,accepted_at) WHERE NOT processed;
CREATE INDEX ops_issue_queue ON incident_issues(workspace_id,approved_at) WHERE status IN ('queued','reconciling');
CREATE INDEX ops_plan_queue ON planned_issues(workspace_id,created_at) WHERE status IN ('queued','reconciling');
CREATE INDEX ops_draft_queue ON draft_pr_intents(workspace_id,created_at) WHERE status IN ('queued','reconciling');
CREATE INDEX ops_control_queue ON controls(workspace_id,created_at) WHERE status IN ('requested','received');
CREATE INDEX ops_recent_attempts ON attempt_reports(workspace_id,updated_at DESC,id);
CREATE INDEX ops_recent_permits ON general_model_turns(workspace_id,ended_at DESC) WHERE ended_at IS NOT NULL;
CREATE INDEX ops_reconnects ON events(workspace_id,created_at DESC) WHERE kind='device.reconnected';
