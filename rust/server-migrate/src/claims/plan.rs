// SPDX-License-Identifier: MPL-2.0
use crate::coord::{Db, Error, Result};
use serde::Deserialize;
use serde_json::Value;

pub(super) const DRIFT: &str =
    "GitHub issue changed, is closed, or lacks a recent authoritative read; review a fresh plan";

pub(super) struct Plan {
    pub id: String,
    pub document: Value,
    pub tickets: Vec<Ticket>,
}

#[derive(Deserialize)]
pub(super) struct Ticket {
    pub number: i64,
    pub priority: i64,
    pub criteria: Vec<String>,
    dependencies: Option<Vec<i64>>,
    resources: Option<Vec<String>>,
}

impl Ticket {
    pub fn resources(&self) -> &[String] {
        self.resources.as_deref().unwrap_or_default()
    }

    pub fn dependencies(&self) -> &[i64] {
        self.dependencies.as_deref().unwrap_or_default()
    }
}

pub(super) fn approved(db: &Db<'_>, ws: &str, project: &str) -> Result<Option<Plan>> {
    let row = db.optional(
        "SELECT id,document FROM plan_versions WHERE workspace_id=$1 AND project_id=$2
        AND approved_at IS NOT NULL ORDER BY version DESC LIMIT 1",
        &[ws, project],
    )?;
    row.map(|r| {
        let document: Value = serde_json::from_str(r.text(1)?).map_err(|_| Error::internal())?;
        let tickets =
            serde_json::from_value(document["tickets"].clone()).map_err(|_| Error::internal())?;
        Ok(Plan {
            id: r.text(0)?.into(),
            document,
            tickets,
        })
    })
    .transpose()
}

// Issues outside the publication system may be admitted manually. Published
// issues must retain their plan-specific generation and recent authoritative read.
pub(super) fn current_work(
    db: &Db<'_>,
    ws: &str,
    project: &str,
    plan: &str,
    number: i64,
) -> Result<bool> {
    // A grant relies on both the running issue and its completed prerequisites.
    // Check and lock all published issue revisions in one database round trip.
    db.one(
        "WITH required(number) AS (
            SELECT $4::bigint UNION
            SELECT unnest(t.dependencies) FROM plan_versions v
            CROSS JOIN LATERAL jsonb_to_recordset(v.document->'tickets') AS t(number bigint, dependencies bigint[])
            WHERE v.workspace_id=$1 AND v.project_id=$2 AND v.id=$3 AND t.number=$4
        )
        SELECT coalesce(bool_and(current_revision),true) FROM (
            SELECT m.active AND z.availability='current' AND (z.source_state='open' OR z.number<>$4 AND z.source_state='closed')
                AND z.checked_at IS NOT NULL AND z.checked_at>now()-interval '20 minutes' AND EXISTS(SELECT FROM plan_issue_bindings b WHERE b.plan_id=$3 AND
                    (b.workspace_id,b.project_id,b.number,b.generation)=(z.workspace_id,z.project_id,z.number,z.generation)) AS current_revision
            FROM required r JOIN planned_issue_state z USING(number)
            JOIN github_mappings m USING(workspace_id,project_id,installation_id,repository_id)
            WHERE z.workspace_id=$1 AND z.project_id=$2 ORDER BY z.number FOR SHARE OF z,m
        ) locked",
        &[ws, project, plan, &number.to_string()],
    )?.bool(0)
}

// This SHA-256 input is the compact Go encoding/json format used by existing
// runner journals and approved plans, including its HTML and JS line escaping.
fn criteria_json(criteria: &[String]) -> Result<String> {
    Ok(serde_json::to_string(criteria)?
        .replace('&', r"\u0026")
        .replace('<', r"\u003c")
        .replace('>', r"\u003e")
        .replace('\u{2028}', r"\u2028")
        .replace('\u{2029}', r"\u2029"))
}

pub(super) fn blocked(
    db: &Db<'_>,
    ws: &str,
    project: &str,
    plan: &str,
    ticket: &Ticket,
    attempt: &str,
    expected_hash: Option<&str>,
) -> Result<Option<&'static str>> {
    if !current_work(db, ws, project, plan, ticket.number)? {
        return Ok(Some(DRIFT));
    }
    if let Some(hash) = expected_hash {
        let encoded = criteria_json(&ticket.criteria)?;
        if !db
            .one(
                "SELECT encode(sha256(convert_to($1,'UTF8')),'hex')=$2::text",
                &[&encoded, hash],
            )?
            .bool(0)?
        {
            return Ok(Some("Local criteria differ from the approved plan"));
        }
    }
    for dep in ticket.dependencies() {
        if !db.one("SELECT EXISTS(SELECT FROM prerequisite_evidence WHERE workspace_id=$1 AND project_id=$2 AND plan_id=$3 AND issue_number=$4)",
            &[ws,project,plan,&dep.to_string()])?.bool(0)? {
            return Ok(Some("Waiting for prerequisite integration evidence"));
        }
    }
    if !ticket.resources().is_empty() {
        let resources = serde_json::to_string(ticket.resources())?;
        if db.one("SELECT EXISTS(SELECT FROM admission WHERE workspace_id=$1 AND owns_issue AND attempt_id<>$2
            AND resources ?| ARRAY(SELECT jsonb_array_elements_text($3::jsonb)))", &[ws, attempt, &resources])?.bool(0)? {
            return Ok(Some("A shared resource is reserved"));
        }
    }
    Ok(None)
}
