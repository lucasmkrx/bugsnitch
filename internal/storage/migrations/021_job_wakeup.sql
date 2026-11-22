-- Wakeups carry no customer data or authority. Each search_path installation
-- listens to its own schema's channel; a timed rescan recovers missed signals.
CREATE FUNCTION wake_background_jobs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_catalog.pg_notify('bugsnitch_jobs_' || pg_catalog.md5(TG_TABLE_SCHEMA), '');
  RETURN NULL;
END $$;

CREATE TRIGGER wake_inbox AFTER INSERT ON incident_inbox
 FOR EACH ROW WHEN (NOT NEW.processed) EXECUTE FUNCTION wake_background_jobs();
CREATE TRIGGER wake_incident_issue AFTER INSERT OR UPDATE OF next_run,status ON incident_issues
 FOR EACH ROW WHEN (NEW.status <> 'blocked' AND NEW.next_run <= clock_timestamp()) EXECUTE FUNCTION wake_background_jobs();
CREATE TRIGGER wake_draft AFTER INSERT OR UPDATE OF next_run,status ON draft_pr_intents
 FOR EACH ROW WHEN (NEW.status IN ('queued','reconciling') AND NEW.next_run <= clock_timestamp()) EXECUTE FUNCTION wake_background_jobs();
CREATE TRIGGER wake_planned_issue AFTER INSERT OR UPDATE OF next_run,status ON planned_issues
 FOR EACH ROW WHEN (NEW.status IN ('queued','reconciling') AND NEW.next_run <= clock_timestamp()) EXECUTE FUNCTION wake_background_jobs();
CREATE TRIGGER wake_drift AFTER INSERT OR UPDATE OF next_check ON planned_issue_state
 FOR EACH ROW WHEN (NEW.next_check <= clock_timestamp()) EXECUTE FUNCTION wake_background_jobs();
CREATE TRIGGER wake_publication AFTER UPDATE OF status ON publications
 FOR EACH ROW WHEN (NEW.status = 'publishing') EXECUTE FUNCTION wake_background_jobs();
CREATE TRIGGER wake_policy AFTER UPDATE OF paused,auto_triage,allow_promotion,allow_publication,policy_version ON projects
 FOR EACH ROW EXECUTE FUNCTION wake_background_jobs();
CREATE TRIGGER wake_mapping AFTER INSERT OR UPDATE OF active ON github_mappings
 FOR EACH ROW WHEN (NEW.active) EXECUTE FUNCTION wake_background_jobs();
