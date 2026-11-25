// SPDX-License-Identifier: MPL-2.0
use crate::coord::{Db, Error, Result};
use serde_json::Value;
use std::sync::LazyLock;

macro_rules! view {
    ($name:literal) => {
        include_str!(concat!("../../../internal/transport/views/", $name, ".sql"))
    };
}

// All fragments are build-time constants. One bound SQL statement reads both the
// ownership row and every section from the same MVCC snapshot.
static STATE: LazyLock<String> = LazyLock::new(|| {
    let mut fields = vec![
        r#"json_build_object('id',owner_scope.id,'name',owner_scope.name,'stopped',owner_scope.stopped,'dispatchVersion',owner_scope.dispatch_version) AS "workspace""#.into(),
        r#"json_build_object('id',coalesce(viewer.id,owner_scope.account_id),'name',coalesce(viewer.name,'Owner'),'role',coalesce(viewer.role,'owner'),'projectId',viewer.project_id) AS "viewer""#.into(),
        r#"json_build_object('tickets',owner_scope.ticket_limit,'turns',owner_scope.turn_limit,'version',owner_scope.limits_version) AS "limits""#.into(),
        r#"json_build_object('hostedInference',false,'reason','Hosted inference requires confirmation of this deployment’s eligibility for Sign in with ChatGPT. Local account-backed runs can be started on the computer.') AS "capabilities""#.into(),
        r#"CASE WHEN viewer.id IS NULL THEN json_build_object('monthlyTurns',owner_scope.monthly_turn_limit,'usedTurns',coalesce((SELECT turns FROM monthly_model_usage WHERE workspace_id=owner_scope.id AND month=date_trunc('month',now() AT TIME ZONE 'UTC')::date),0)) ELSE 'null'::json END AS "budget""#.into(),
        r#"CASE WHEN viewer.id IS NULL THEN (SELECT coalesce(json_agg(q),'[]') FROM (SELECT r.id,r.name,r.environment,r.cancelled_at AS "cancelledAt",
          (SELECT json_agg(json_build_object('projectId',s.project_id,'number',s.pull_number,'status',s.status,'head',s.head,'target',s.target) ORDER BY s.position)
           FROM release_stage_status s WHERE s.workspace_id=r.workspace_id AND s.release_id=r.id) AS stages
          FROM release_plans r WHERE r.workspace_id=owner_scope.id ORDER BY r.created_at DESC,r.id LIMIT 60) q) ELSE '[]'::json END AS releases"#.into(),
        r#"to_json(now()) AS "updatedAt""#.into(),
    ];
    let events = format!(
        "SELECT {} FROM events WHERE workspace_id=$1 ORDER BY position DESC LIMIT 150",
        crate::events::FIELDS
    );
    let pulls = format!(
        r#"{} ORDER BY o.observed_at DESC,o.project_id,o.number LIMIT 500"#,
        view!("pulls")
    );
    for (key, query, document) in [
        ("projects", view!("projects"), false),
        ("publications", view!("publications"), false),
        ("devices", view!("devices"), false),
        ("decisions", view!("decisions"), false),
        ("messages", view!("messages"), false),
        ("attempts", view!("attempts"), false),
        ("tickets", view!("tickets"), false),
        ("events", &events[..], false),
        ("incidents", view!("incidents"), true),
        ("pullRequests", &pulls[..], true),
        ("draftPRs", view!("drafts"), true),
    ] {
        let aggregate = if document {
            "CASE WHEN bool_or(t.document IS NULL) THEN NULL ELSE coalesce(json_agg(t.document),'[]') END"
        } else {
            "coalesce(json_agg(t),'[]')"
        };
        fields.push(format!(
            r#"(SELECT {aggregate} FROM ({query}) t) AS "{key}""#
        ));
    }
    // NULL identifies an unsupported persisted document shape. A projection
    // error fails closed without leaking the unchecked value into the response.
    fields.push(format!(
        r#"(SELECT CASE WHEN bool_or(t.document IS NULL) THEN NULL
        ELSE coalesce(json_agg(t),'[]') END FROM ({}) t) AS plans"#,
        view!("plans")
    ));
    session_query(&format!("SELECT {}", fields.join(",")), false)
});

static DIAGNOSTICS: LazyLock<String> = LazyLock::new(|| session_query(view!("diagnostics"), true));

// Scope every fragment to the workspace derived from the live browser session.
// The only wire parameter is the session hash; a caller cannot nominate an account or workspace.
fn session_query(projection: &str, owner_only: bool) -> String {
    let access = if owner_only {
        " AND s.member_id IS NULL"
    } else {
        " AND (s.member_id IS NULL OR viewer.revoked_at IS NULL)"
    };
    // Apply the session's project restriction before each section's ordering and
    // row limit. All detail subqueries join through these visible project rows.
    let mut scope = String::new();
    if !owner_only {
        let mut tables = vec!["projects AS NOT MATERIALIZED (SELECT * FROM projects WHERE viewer.project_id IS NULL OR id=viewer.project_id)".to_owned()];
        for table in [
            "device_projects",
            "events",
            "decisions",
            "publications",
            "manager_messages",
            "attempt_reports",
            "plan_versions",
            "incidents",
            "incident_issues",
            "planned_issue_state",
            "pull_observations",
            "draft_pr_intents",
        ] {
            tables.push(format!("{table} AS NOT MATERIALIZED (SELECT * FROM {table} WHERE viewer.project_id IS NULL OR project_id=viewer.project_id)"));
        }
        tables.push("devices AS NOT MATERIALIZED (SELECT d.* FROM devices d WHERE viewer.project_id IS NULL OR EXISTS(SELECT FROM device_projects dp WHERE dp.device_id=d.id))".into());
        scope = format!("WITH {} ", tables.join(","));
    }
    format!(
        "SELECT dashboard.* FROM sessions s JOIN workspaces owner_scope ON owner_scope.id=s.workspace_id \
            LEFT JOIN workspace_members viewer ON (viewer.workspace_id,viewer.id)=(s.workspace_id,s.member_id) \
            CROSS JOIN LATERAL ({scope}{}) dashboard WHERE s.token_hash=$1 AND s.expires_at>now(){access}",
        projection.replace("$1", "owner_scope.id")
    )
}

pub fn read(db: &Db<'_>, op: &str, input: Value) -> Result<String> {
    if db.actor() != ("service", "http-server") {
        return Err(Error::denied());
    }
    let hash = input
        .as_str()
        .ok_or_else(|| Error::bad("Invalid session hash"))?;
    let sql = match op {
        "workspace.state" => STATE.as_str(),
        "workspace.diagnostics" => DIAGNOSTICS.as_str(),
        _ => return Err(Error::bad("Unknown workspace projection")),
    };
    let r = db.projection(op, sql, hash)?;
    if r.ntuples() == 0 {
        return Err(Error::unauthenticated());
    }
    if r.ntuples() != 1 {
        return Err(Error::internal());
    }

    // PostgreSQL emits a JSON value per column. Assemble the object once; a
    // json_build_object around all sections would copy even multi-MiB plan documents again.
    let mut body = Vec::with_capacity((0..r.nfields()).map(|n| r.length(0, n) + 32).sum());
    body.push(b'{');
    for n in 0..r.nfields() {
        if n > 0 {
            body.push(b',');
        }
        let name = r.field_name(n).ok().flatten().ok_or_else(Error::internal)?;
        serde_json::to_writer(&mut body, &name).map_err(|_| Error::internal())?;
        body.push(b':');
        body.extend_from_slice(r.value(0, n).ok_or_else(Error::internal)?);
    }
    body.push(b'}');
    String::from_utf8(body).map_err(|_| Error::internal())
}
