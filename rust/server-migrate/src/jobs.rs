// SPDX-License-Identifier: MPL-2.0
use crate::coord::{Db, Details, Error, Result};
use serde::Deserialize;
use serde_json::{Value, json};

// Reserve one due row without holding a native connection across provider I/O.
// The Go consumer takes that row's lock and rechecks the returned timestamp and
// current authorization before any provider call. An unused lease expires under
// both the new worker and the previous release's due-time polling rules.
const INCIDENT_ISSUE: &str = "
    WITH ready AS (
        SELECT x.incident_id FROM incident_issues x
        JOIN github_mappings m USING(workspace_id,project_id,installation_id,repository_id)
        JOIN projects p ON (p.workspace_id,p.id)=(x.workspace_id,x.project_id)
        WHERE m.active AND x.status<>'blocked' AND x.next_run<=now()
        AND (x.status<>'queued' OR (NOT p.paused AND p.allow_promotion AND x.policy_version=p.policy_version))
        ORDER BY x.next_run,x.incident_id FOR UPDATE OF x SKIP LOCKED LIMIT 1
    )
    UPDATE incident_issues x SET next_run=clock_timestamp()+interval '1 minute'
    FROM ready WHERE x.incident_id=ready.incident_id
    RETURNING x.workspace_id,x.project_id,x.incident_id,0,x.next_run::text";

const DRAFT: &str = "
    WITH ready AS (
        SELECT x.workspace_id,x.attempt_id FROM draft_pr_intents x
        JOIN github_mappings m USING(workspace_id,project_id,installation_id,repository_id)
        WHERE m.active AND x.status IN ('queued','reconciling') AND x.next_run<=now()
        ORDER BY x.next_run,x.attempt_id,x.workspace_id FOR UPDATE OF x SKIP LOCKED LIMIT 1
    )
    UPDATE draft_pr_intents x SET next_run=clock_timestamp()+interval '1 minute'
    FROM ready WHERE (x.workspace_id,x.attempt_id)=(ready.workspace_id,ready.attempt_id)
    RETURNING x.workspace_id,x.project_id,x.attempt_id,0,x.next_run::text";

const PLANNED_ISSUE: &str = "
    WITH ready AS (
        SELECT i.publication_id,i.local_number FROM planned_issues i
        JOIN publications x ON (x.workspace_id,x.project_id,x.id)=(i.workspace_id,i.project_id,i.publication_id)
        JOIN github_mappings m ON (m.workspace_id,m.project_id,m.installation_id,m.repository_id)
                                 =(x.workspace_id,x.project_id,x.installation_id,x.repository_id)
        JOIN projects p ON (p.workspace_id,p.id)=(i.workspace_id,i.project_id)
        WHERE m.active AND NOT p.paused AND i.status IN ('queued','reconciling') AND x.status='publishing'
        AND (i.status='reconciling' OR (p.allow_publication AND x.policy_version=p.policy_version))
        AND i.next_run<=now() AND NOT EXISTS(
            SELECT FROM planned_issues prev WHERE prev.publication_id=i.publication_id
            AND prev.position<i.position AND prev.status<>'linked')
        ORDER BY i.next_run,i.publication_id,i.position FOR UPDATE OF i SKIP LOCKED LIMIT 1
    )
    UPDATE planned_issues i SET next_run=clock_timestamp()+interval '1 minute' FROM ready
    WHERE (i.publication_id,i.local_number)=(ready.publication_id,ready.local_number)
    RETURNING i.workspace_id,i.project_id,i.publication_id,i.local_number,i.next_run::text";

const DRIFT: &str = "
    WITH ready AS (
        SELECT z.workspace_id,z.project_id,z.number FROM planned_issue_state z
        JOIN github_mappings m USING(workspace_id,project_id,installation_id,repository_id)
        WHERE m.active AND z.next_check<=now()
        ORDER BY z.next_check,z.project_id,z.number FOR UPDATE OF z SKIP LOCKED LIMIT 1
    )
    UPDATE planned_issue_state z SET next_check=clock_timestamp()+interval '1 minute' FROM ready
    WHERE (z.workspace_id,z.project_id,z.number)=(ready.workspace_id,ready.project_id,ready.number)
    RETURNING z.workspace_id,z.project_id,''::text,z.number,z.next_check::text";

const PULL_WATCH: &str = "
    WITH ready AS (
        SELECT o.workspace_id,o.project_id,o.number FROM pull_observations o
        JOIN github_mappings m USING(workspace_id,project_id)
        WHERE m.active AND o.watching AND o.snapshot->>'state'='OPEN' AND o.next_watch<=now()
        ORDER BY o.next_watch,o.workspace_id,o.project_id,o.number FOR UPDATE OF o SKIP LOCKED LIMIT 1
    )
    UPDATE pull_observations o SET next_watch=clock_timestamp()+interval '2 minutes' FROM ready
    WHERE (o.workspace_id,o.project_id,o.number)=(ready.workspace_id,ready.project_id,ready.number)
    RETURNING o.workspace_id,o.project_id,''::text,o.number,o.next_watch::text";

// The same eligibility query drives both the cheap readiness hint and the locked
// qualification operation. Another server may consume a hinted row first.
macro_rules! qualifying {
    () => {
        "SELECT i.workspace_id,i.project_id,i.id,i.version FROM incidents i
         JOIN projects p ON (p.workspace_id,p.id)=(i.workspace_id,i.project_id)
         JOIN github_mappings m ON (m.workspace_id,m.project_id)=(i.workspace_id,i.project_id)
         WHERE i.source='sentry' AND i.state='Needs reproduction' AND i.occurrences>=3
         AND p.auto_triage AND p.allow_promotion AND NOT p.paused AND m.active
         AND NOT EXISTS(SELECT FROM incident_issues WHERE incident_id=i.id)
         AND EXISTS(SELECT FROM incident_inbox b WHERE (b.workspace_id,b.project_id,b.source,b.group_key)
                                      =(i.workspace_id,i.project_id,i.source,i.group_key)
             AND b.processed AND b.environment<>'' AND b.release<>'' AND b.seen_at BETWEEN now()-interval '1 day' AND now()
             GROUP BY b.environment,b.release HAVING count(*)>=3)
         ORDER BY i.first_seen,i.id"
    };
}

#[derive(Deserialize, PartialEq)]
#[serde(rename_all = "snake_case")]
enum Lane {
    Inbox,
    Triage,
    IncidentIssue,
    Draft,
    PlannedIssue,
    Drift,
    PullWatch,
}

impl Lane {
    // Only these fixed identifiers and fixed SQL fragments enter the query.
    fn selection(&self) -> (&'static str, &'static str) {
        match self {
            Self::Inbox => (
                "inbox",
                "SELECT 1 WHERE EXISTS(SELECT FROM incident_inbox WHERE NOT processed)",
            ),
            Self::Triage => (
                "triage",
                concat!("SELECT 1 WHERE EXISTS(", qualifying!(), ")"),
            ),
            Self::IncidentIssue => ("incident_issue", INCIDENT_ISSUE),
            Self::Draft => ("draft", DRAFT),
            Self::PlannedIssue => ("planned_issue", PLANNED_ISSUE),
            Self::Drift => ("drift", DRIFT),
            Self::PullWatch => ("pull_watch", PULL_WATCH),
        }
    }
}

fn schedule(db: &Db<'_>, input: Value) -> Result<Value> {
    let lanes: Vec<Lane> = serde_json::from_value(input)?;
    if lanes.len() > 7 {
        return Err(Error::bad("Too many background queues"));
    }
    if lanes.is_empty() {
        return Ok(json!([]));
    }
    let mut selections = Vec::new();
    let mut output = Vec::new();
    for (i, lane) in lanes.iter().enumerate() {
        if lanes[..i].contains(lane) {
            return Err(Error::bad("Duplicate background queue"));
        }
        let (name, query) = lane.selection();
        if matches!(lane, Lane::Inbox | Lane::Triage) {
            selections.push(format!("{name} AS ({query})"));
            output.push(format!("SELECT '{name}', '', '', '', 0, '' FROM {name}"));
        } else {
            selections.push(format!(
                "{name}(workspace,project,item,number,fence) AS ({query})"
            ));
            output.push(format!(
                "SELECT '{name}',workspace,project,item,number,fence FROM {name}"
            ));
        }
    }
    // Each free provider lane reserves at most one row. The worker rechecks its
    // lease and authority after this commits. Inbox work has its own transaction
    // so a bad observation cannot roll back reservations for unrelated jobs.
    let sql = format!(
        "WITH {} {}",
        selections.join(","),
        output.join(" UNION ALL ")
    );
    let mut jobs = Vec::new();
    for row in db.rows(&sql, &[])? {
        jobs.push(
            json!({"lane": row.text(0)?, "workspace": row.text(1)?, "project": row.text(2)?,
            "item": row.text(3)?, "number": row.i64(4)?, "fence": row.text(5)?}),
        );
    }
    Ok(json!(jobs))
}

pub fn handle(db: &Db<'_>, op: &str, input: Value) -> Result<Value> {
    if op == "jobs.schedule" {
        return schedule(db, input);
    }
    if !input.as_object().is_some_and(|o| o.is_empty()) {
        return Err(Error::bad("Job selection accepts no arguments"));
    }
    let sql = match op {
        "jobs.claim_incident_issue" => INCIDENT_ISSUE,
        "jobs.claim_draft" => DRAFT,
        "jobs.claim_planned_issue" => PLANNED_ISSUE,
        "jobs.claim_drift" => DRIFT,
        "jobs.claim_pull_watch" => PULL_WATCH,
        "jobs.process_incident" => return process_incident(db),
        "jobs.qualify_incident" => return qualify_incident(db),
        _ => return Err(Error::bad("Unknown background job")),
    };
    let Some(row) = db.optional(sql, &[])? else {
        return Ok(Value::Null);
    };
    Ok(json!({
        "workspace": row.text(0)?, "project": row.text(1)?, "item": row.text(2)?,
        "number": row.i64(3)?, "fence": row.text(4)?,
    }))
}

fn process_incident(db: &Db<'_>) -> Result<Value> {
    let Some(row) = db.optional(
        "SELECT id FROM incident_inbox WHERE NOT processed ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1",
        &[],
    )? else {
        return Ok(json!(false));
    };
    let inbox = row.text(0)?;
    let incident = db.one(
        include_str!("../../../internal/incidents/sql/apply_observation.sql"),
        &[inbox],
    )?;
    db.event(
        incident.text(1)?,
        incident.text(2)?,
        "incident.observed",
        incident.text(0)?,
        ("service", "incident-worker"),
        Details {
            reason: "Accepted observation assigned to its incident",
            evidence: inbox,
            ..Default::default()
        },
    )?;
    Ok(json!(true))
}

fn qualify_incident(db: &Db<'_>) -> Result<Value> {
    let Some(incident) = db.optional(
        concat!(qualifying!(), " FOR UPDATE OF i SKIP LOCKED LIMIT 1"),
        &[],
    )?
    else {
        return Ok(json!(false));
    };
    let (ws, project, id, version) = (
        incident.text(0)?,
        incident.text(1)?,
        incident.text(2)?,
        incident.text(3)?,
    );
    let Some(intent) = db.optional(
        "INSERT INTO incident_issues(workspace_id,project_id,incident_id,installation_id,repository_id,marker,approved_version,status,policy_version)
         SELECT m.workspace_id,m.project_id,$3,m.installation_id,m.repository_id,replace(gen_random_uuid()::text,'-',''),$4,'queued',p.policy_version
         FROM github_mappings m JOIN projects p ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id)
         WHERE m.workspace_id=$1 AND m.project_id=$2 AND m.active AND p.auto_triage AND p.allow_promotion AND NOT p.paused
         FOR SHARE OF m,p ON CONFLICT DO NOTHING RETURNING policy_version",
        &[ws, project, id, version],
    )? else {
        return Ok(json!(false));
    };
    db.exec("UPDATE incidents SET version=version+1 WHERE id=$1", &[id])?;
    db.event(ws, project, "incident.policy_qualified", id, ("service","incident-worker"), Details {
        reason: "Three recent events on the same build and environment qualified under project policy",
        version: incident.i64(3)?, policy: intent.i64(0)?, ..Default::default()
    })?;
    Ok(json!(true))
}
