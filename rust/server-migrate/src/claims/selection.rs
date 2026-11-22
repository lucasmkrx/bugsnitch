// SPDX-License-Identifier: MPL-2.0
use super::{capacity, plan};
use crate::coord::{Db, Error, Result};
use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Deserialize)]
struct Next {
    #[serde(rename = "projectId")]
    project: String,
}

// This uses a repeatable-read transaction. Selection explains what could be
// admitted. Only a subsequent claim can reserve capacity or issue ownership.
pub(super) fn next(db: &Db<'_>, token: &str, request: Value) -> Result<Value> {
    let Next { project } = serde_json::from_value(request)?;
    let row = db.optional("SELECT w.id,d.id,p.repository,p.policy_version,p.paused,w.stopped,w.ticket_limit,w.turn_limit,d.turn_limit,
        EXISTS(SELECT FROM github_mappings g WHERE g.workspace_id=w.id AND g.project_id=p.id AND g.active),
        (SELECT count(*) FROM admission a WHERE a.workspace_id=w.id AND a.active)
        FROM device_projects m JOIN devices d ON (m.workspace_id,m.device_id)=(d.workspace_id,d.id) JOIN workspaces w ON w.id=d.workspace_id
        JOIN projects p ON (m.workspace_id,m.project_id)=(p.workspace_id,p.id)
        WHERE p.id=$1 AND NOT d.revoked AND d.token_hash=$2", &[&project,token])?.ok_or_else(Error::denied)?;
    let ws = row.text(0)?;
    let device = row.text(1)?;
    let repository = row.text(2)?;
    let reason = if row.bool(5)? {
        "Workspace dispatch is stopped"
    } else if row.bool(4)? {
        "Project dispatch is paused"
    } else if !row.bool(9)? {
        "The project needs an active GitHub App repository mapping"
    } else if row.i64(10)? >= row.i64(6)? {
        "All workspace implementation slots are reserved"
    } else if !capacity(db, ws, device, row.i64(7)?, row.i64(8)?)? {
        "All workspace or device model-turn slots are reserved"
    } else {
        ""
    };
    if !reason.is_empty() {
        return Ok(json!({"reason":reason}));
    }
    let Some(mut plan) = plan::approved(db, ws, &project)? else {
        return Ok(json!({"reason":"Approve a plan before requesting its next issue"}));
    };
    plan.tickets.sort_by_key(|t| (t.priority, t.number));
    let mut candidates = Vec::with_capacity(plan.tickets.len());
    for ticket in &plan.tickets {
        candidates.push(candidate(db, ws, &project, &plan.id, repository, ticket)?);
    }
    Ok(
        json!({"plan":plan.document,"scope":{"deviceId":device,"projectId":project,"planId":plan.id,"policyVersion":row.i64(3)?},"candidates":candidates}),
    )
}

fn candidate(
    db: &Db<'_>,
    ws: &str,
    project: &str,
    plan: &str,
    repository: &str,
    ticket: &plan::Ticket,
) -> Result<Value> {
    let n = ticket.number.to_string();
    let blocked = |reason: &str| json!({"number":ticket.number,"reason":reason});
    let state = db.one("SELECT EXISTS(SELECT FROM admission WHERE workspace_id=$1 AND project_id=$2 AND issue_number=$3),
        EXISTS(SELECT FROM prerequisite_evidence WHERE workspace_id=$1 AND project_id=$2 AND issue_number=$3 AND plan_id=$4)", &[ws,project,&n,plan])?;
    if state.bool(0)? {
        return Ok(blocked(
            "an attempt already exists; inspect or explicitly retry it",
        ));
    }
    if state.bool(1)? {
        return Ok(blocked("the plan already records integration evidence"));
    }
    if let Some(reason) = plan::blocked(db, ws, project, plan, ticket, "", None)? {
        return Ok(blocked(reason));
    }
    if !ticket.dependencies().is_empty() {
        let deps = serde_json::to_string(ticket.dependencies())?;
        if db.one("SELECT EXISTS(SELECT FROM jsonb_array_elements_text($4::jsonb) dep(value)
            WHERE NOT EXISTS(SELECT FROM plan_issue_bindings b JOIN planned_issue_state z USING(workspace_id,project_id,number,generation)
            WHERE b.workspace_id=$1 AND b.project_id=$2 AND b.plan_id=$3 AND b.number=dep.value::bigint))", &[ws,project,plan,&deps])?.bool(0)? {
            return Ok(blocked("a prerequisite lacks a verified canonical issue revision in this plan"));
        }
    }
    let Some(issue) = db.optional(
        "SELECT z.github_id,z.digest,z.title,z.selection_revision FROM planned_issue_state z
        JOIN plan_issue_bindings b USING (workspace_id,project_id,number,generation)
        WHERE b.plan_id=$1 AND z.workspace_id=$2 AND z.project_id=$3 AND z.number=$4",
        &[plan, ws, project, &n],
    )?
    else {
        return Ok(blocked(
            "the approved plan lacks a verified canonical GitHub issue revision",
        ));
    };
    let revision = issue.text(3)?;
    let digest = issue.text(1)?;
    if revision.is_empty() || digest.len() != 64 {
        return Ok(blocked(
            "awaiting a canonical GitHub timestamp and a title suitable for sharing",
        ));
    }
    Ok(json!({"number":ticket.number, "selection":{
        "githubId":issue.i64(0)?, "requirementsDigest":digest, "schema":1, "repository":repository,
        "number":ticket.number, "url":format!("https://github.com/{repository}/issues/{n}"),
        "title":issue.text(2)?, "state":"open", "updatedAt":revision
    }}))
}
