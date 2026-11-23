// SPDX-License-Identifier: MPL-2.0
//! Hosted admission, capacity and original-machine fences. The caller commits
//! the operation and its audit event together before replying to the runner.
mod plan;
mod selection;

use crate::coord::{Db, Details, Error, Result};
use crate::devices::{lower_hex, valid_key, valid_sha};
use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Envelope {
    token_hash: String,
    request: Value,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Scope {
    device_id: String,
    project_id: String,
    plan_id: String,
    policy_version: i64,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Admission {
    project_id: String,
    attempt_id: String,
    base_sha: String,
    issue_number: i64,
    sequence: i64,
    operation: String,
    #[serde(default)]
    criteria_hash: String,
    #[serde(default)]
    state: String,
    scope: Option<Scope>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Permit {
    workspace_id: String,
    device_id: String,
    permit_id: String,
    role: String,
    #[serde(default)]
    project_id: String,
    version: i64,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Lease {
    workspace_id: String,
    device_id: String,
    project_id: String,
    attempt_id: String,
    base_sha: String,
    issue_number: i64,
    sequence: i64,
    subject_version: i64,
    #[serde(default)]
    model_turn: bool,
    scope: Option<Scope>,
}

pub fn handle(db: &Db<'_>, op: &str, input: Value) -> Result<Value> {
    let Envelope {
        token_hash,
        mut request,
    } = serde_json::from_value(input)?;
    if request.as_object_mut().and_then(|v| v.remove("protocol")) != Some(json!(1)) {
        return Err(Error::bad("Invalid claim protocol"));
    }
    match op {
        "claims.admission" => admit(db, &token_hash, serde_json::from_value(request)?),
        "claims.permit" => permit(db, &token_hash, serde_json::from_value(request)?),
        "claims.lease" => lease(db, &token_hash, serde_json::from_value(request)?),
        "claims.next" => selection::next(db, &token_hash, request),
        _ => Err(Error::bad("Unknown claim operation")),
    }
}

fn capacity(
    db: &Db<'_>,
    ws: &str,
    device: &str,
    workspace_limit: i64,
    device_limit: i64,
) -> Result<bool> {
    let counts = db.one("SELECT count(*),count(*) FILTER(WHERE device_id=$2) FROM
        (SELECT device_id FROM admission WHERE workspace_id=$1 AND in_turn
         UNION ALL SELECT device_id FROM general_model_turns WHERE workspace_id=$1 AND version=1) AS occupied",
        &[ws,device])?;
    Ok(counts.i64(0)? < workspace_limit && counts.i64(1)? < device_limit)
}

fn admit(db: &Db<'_>, token: &str, a: Admission) -> Result<Value> {
    if !valid_key(&a.attempt_id)
        || !valid_sha(&a.base_sha)
        || a.issue_number < 1
        || a.sequence < 1
        || !matches!(
            a.operation.as_str(),
            "admit" | "finish" | "turn_start" | "turn_end" | "turn_abandon"
        )
    {
        return Err(Error::bad("Invalid admission request"));
    }
    if a.operation == "finish"
        && !matches!(
            a.state.as_str(),
            "waiting" | "completed" | "failed" | "cancelled"
        )
    {
        return Err(Error::bad("Provide the demonstrated local outcome"));
    }
    // All claim writers acquire device, workspace, then project locks in the
    // same order. Competing devices serialize workspace-wide limits here.
    let actor = db
        .optional(
            "SELECT d.id,d.workspace_id,d.turn_limit FROM devices d
        JOIN device_projects m ON (m.workspace_id,m.device_id)=(d.workspace_id,d.id)
        WHERE d.token_hash=$1 AND NOT d.revoked AND m.project_id=$2 FOR NO KEY UPDATE OF d",
            &[token, &a.project_id],
        )?
        .ok_or_else(Error::denied)?;
    let dev = actor.text(0)?;
    let ws = actor.text(1)?;
    let limits = db.one(
        "SELECT ticket_limit,turn_limit,stopped FROM workspaces WHERE id=$1 FOR NO KEY UPDATE",
        &[ws],
    )?;
    let project = db.one("SELECT paused,require_plan,policy_version FROM projects WHERE workspace_id=$1 AND id=$2 FOR SHARE", &[ws,&a.project_id])?;
    let policy = project.i64(2)?;
    let start = matches!(a.operation.as_str(), "admit" | "turn_start");
    if start {
        if let Some(s) = &a.scope {
            if s.device_id != dev || s.project_id != a.project_id {
                return Err(Error::denied());
            }
            if s.plan_id.is_empty() || s.policy_version != policy {
                return Err(Error::conflict("Local run plan or policy changed"));
            }
        }
        if limits.bool(2)? || project.bool(0)? {
            return Err(Error::conflict(
                "Dispatch is stopped for this workspace or project",
            ));
        }
    }
    let current = db.optional("SELECT sequence,operation,device_id,project_id,base_sha,issue_number,active,in_turn,coalesce(plan_id,''),owns_issue
        FROM admission WHERE workspace_id=$1 AND attempt_id=$2", &[ws,&a.attempt_id])?;
    let receipt = json!({"sequence":a.sequence});
    let (mut active, mut in_turn, mut plan_id) = if let Some(row) = current.as_ref() {
        if row.text(2)? != dev
            || row.text(3)? != a.project_id
            || row.text(4)? != a.base_sha
            || row.i64(5)? != a.issue_number
        {
            return Err(Error::denied());
        }
        let seq = row.i64(0)?;
        if seq == a.sequence && row.text(1)? == a.operation {
            if a.operation == "finish" && row.bool(9)? != (a.state == "waiting") {
                return Err(Error::conflict(
                    "Finish replay has a different ownership outcome",
                ));
            }
            return Ok(receipt);
        }
        if a.operation == "turn_abandon" {
            // The original device has stopped locally. Its start may have
            // committed, or may still be in transit. Fence *that* start in
            // either case so a delayed request cannot acquire a turn.
            let matches_start = if row.bool(7)? {
                row.text(1)? == "turn_start" && seq.checked_add(1) == Some(a.sequence)
            } else {
                seq.checked_add(2) == Some(a.sequence)
            };
            if !row.bool(6)? || !matches_start {
                return Err(Error::conflict("No matching turn start to abandon"));
            }
        } else if seq.checked_add(1) != Some(a.sequence) {
            return Err(Error::conflict(
                "Stale operation sequence; reconcile the original machine",
            ));
        }
        if !row.bool(9)? {
            return Err(Error::conflict(
                "Attempt has already released issue ownership",
            ));
        }
        (row.bool(6)?, row.bool(7)?, row.text(8)?.to_owned())
    } else {
        if a.operation != "admit" || a.sequence != 1 {
            return Err(Error::conflict("Attempt has no matching initial admission"));
        }
        (false, false, String::new())
    };
    let mut resources = None;
    match a.operation.as_str() {
        "admit" => {
            let accepted = plan::approved(db, ws, &a.project_id)?;
            if let Some(plan) = accepted {
                let ticket = plan
                    .tickets
                    .iter()
                    .find(|t| t.number == a.issue_number)
                    .ok_or_else(|| Error::conflict("Issue is outside approved scope"))?;
                if let Some(blocked) = plan::blocked(
                    db,
                    ws,
                    &a.project_id,
                    &plan.id,
                    ticket,
                    &a.attempt_id,
                    Some(&a.criteria_hash),
                )? {
                    return Err(Error::conflict(blocked));
                }
                resources = Some(serde_json::to_string(ticket.resources())?);
                plan_id = plan.id;
            } else {
                if !plan::current_work(db, ws, &a.project_id, "", a.issue_number)? {
                    return Err(Error::conflict(plan::DRIFT));
                }
                plan_id.clear();
                resources = Some("[]".into());
            }
            if a.scope.as_ref().is_some_and(|s| s.plan_id != plan_id) {
                return Err(Error::conflict("Selected plan is no longer approved"));
            }
            if project.bool(1)? && plan_id.is_empty() {
                return Err(Error::conflict(
                    "Approve project scope before starting this issue",
                ));
            }
            if !active {
                let used=db.one("SELECT count(*) FILTER(WHERE active),COALESCE(bool_or(owns_issue AND attempt_id<>$4 AND project_id=$2 AND issue_number=$3),false)
                    FROM admission WHERE workspace_id=$1", &[ws,&a.project_id,&a.issue_number.to_string(),&a.attempt_id])?;
                if used.i64(0)? >= limits.i64(0)? || used.bool(1)? {
                    return Err(Error::conflict(
                        "Ticket capacity or issue ownership is already reserved",
                    ));
                }
            }
            active = true;
        }
        "turn_start" => {
            if a.scope.as_ref().is_some_and(|s| s.plan_id != plan_id) {
                return Err(Error::conflict("Plan differs from the original admission"));
            }
            if !plan::current_work(db, ws, &a.project_id, &plan_id, a.issue_number)? {
                return Err(Error::conflict(plan::DRIFT));
            }
            if !active || in_turn {
                return Err(Error::conflict("Model turn unavailable for this attempt"));
            }
            if !capacity(db, ws, dev, limits.i64(1)?, actor.i64(2)?)? {
                return Err(Error::model_turn_capacity());
            }
            in_turn = true;
        }
        "turn_end" | "turn_abandon" => {
            if a.operation == "turn_end" && !in_turn {
                return Err(Error::conflict("No matching active turn"));
            }
            in_turn = false;
        }
        "finish" => {
            if in_turn {
                return Err(Error::conflict(
                    "Confirm the turn ended before releasing execution capacity",
                ));
            }
            active = false;
        }
        _ => return Err(Error::bad("Invalid admission operation")),
    }
    let owns = a.operation != "finish" || a.state == "waiting";
    db.exec("INSERT INTO admission(workspace_id,project_id,device_id,attempt_id,issue_number,base_sha,sequence,operation,active,in_turn,owns_issue)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(workspace_id,attempt_id) DO UPDATE SET
        sequence=EXCLUDED.sequence,operation=EXCLUDED.operation,owns_issue=EXCLUDED.owns_issue,active=EXCLUDED.active,in_turn=EXCLUDED.in_turn,updated_at=now()",
        &[ws,&a.project_id,dev,&a.attempt_id,&a.issue_number.to_string(),&a.base_sha,&a.sequence.to_string(),&a.operation,&active.to_string(),&in_turn.to_string(),&owns.to_string()])?;
    if let Some(locks) = resources {
        db.exec("UPDATE admission SET plan_id=NULLIF($1,''),resources=$2 WHERE workspace_id=$3 AND attempt_id=$4", &[&plan_id,&locks,ws,&a.attempt_id])?;
    }
    db.event(
        ws,
        &a.project_id,
        &format!("admission.{}", a.operation),
        &a.attempt_id,
        ("device", dev),
        Details {
            reason: "Original device reconciled its execution admission",
            version: a.sequence,
            policy,
            evidence: &a.base_sha,
        },
    )?;
    Ok(receipt)
}

fn permit(db: &Db<'_>, token: &str, a: Permit) -> Result<Value> {
    let valid_project = match a.role.as_str() {
        "manager" => a.project_id.len() == 32 && lower_hex(&a.project_id),
        "planner" | "proof" => a.project_id.is_empty(),
        _ => false,
    };
    if a.permit_id.len() != 32
        || !lower_hex(&a.permit_id)
        || a.device_id.len() != 32
        || !lower_hex(&a.device_id)
        || a.workspace_id.is_empty()
        || a.workspace_id.len() > 100
        || !matches!(a.version, 1 | 2)
        || !valid_project
    {
        return Err(Error::bad("Invalid local model permit"));
    }
    let ws = &a.workspace_id;
    let dev = &a.device_id;
    let actor=db.optional("SELECT turn_limit FROM devices WHERE id=$1 AND workspace_id=$2 AND token_hash=$3 AND NOT revoked FOR NO KEY UPDATE", &[dev,ws,token])?.ok_or_else(Error::denied)?;
    let workspace = db.one(
        "SELECT turn_limit,stopped FROM workspaces WHERE id=$1 FOR NO KEY UPDATE",
        &[ws],
    )?;
    if workspace.bool(1)? && a.version == 1 {
        return Err(Error::conflict("Workspace emergency stop is active"));
    }
    if !a.project_id.is_empty() {
        let project=db.optional("SELECT p.paused FROM projects p JOIN device_projects m ON m.project_id=p.id AND m.workspace_id=p.workspace_id
            WHERE p.id=$1 AND p.workspace_id=$2 AND m.device_id=$3 FOR SHARE OF p", &[&a.project_id,ws,dev])?.ok_or_else(Error::denied)?;
        if project.bool(0)? && a.version == 1 {
            return Err(Error::conflict("Project dispatch is paused"));
        }
    }
    let receipt = json!({"permitId":a.permit_id,"version":a.version});
    let existing=db.optional("SELECT version,role,device_id,COALESCE(project_id,'') FROM general_model_turns WHERE workspace_id=$1 AND permit_id=$2", &[ws,&a.permit_id])?;
    if let Some(row) = existing {
        if row.text(1)? != a.role || row.text(2)? != dev || row.text(3)? != a.project_id {
            return Err(Error::denied());
        }
        if row.i64(0)? == a.version {
            return Ok(receipt);
        }
        if row.i64(0)? == 2 {
            return Err(Error::conflict("model permit already ended"));
        }
        db.exec("UPDATE general_model_turns SET version=2,ended_at=now() WHERE workspace_id=$1 AND permit_id=$2 AND version=1",&[ws,&a.permit_id])?;
    } else {
        if a.version == 1 && !capacity(db, ws, dev, workspace.i64(0)?, actor.i64(0)?)? {
            return Err(Error::conflict(
                "active model turns occupy all available capacity",
            ));
        }
        // An end arriving before an acquisition creates a durable tombstone.
        db.exec("INSERT INTO general_model_turns(workspace_id,device_id,permit_id,role,project_id,version,ended_at)
            VALUES($1,$2,$3,$4,NULLIF($5,''),$6,CASE WHEN $6::integer=2 THEN now() END)", &[ws,dev,&a.permit_id,&a.role,&a.project_id,&a.version.to_string()])?;
    }
    let kind = if a.version == 1 {
        "local_model.started"
    } else {
        "local_model.ended"
    };
    db.event(
        ws,
        &a.project_id,
        kind,
        &a.permit_id,
        ("device", dev),
        Details {
            reason: "Enrolled device reconciled a local model permit",
            version: a.version,
            ..Default::default()
        },
    )?;
    Ok(receipt)
}

fn lease(db: &Db<'_>, token: &str, a: Lease) -> Result<Value> {
    if !valid_key(&a.attempt_id)
        || a.issue_number < 1
        || !valid_sha(&a.base_sha)
        || a.sequence < 1
        || a.subject_version < 1
    {
        return Err(Error::bad("Invalid worker lease subject"));
    }
    let row=db.optional("SELECT coalesce(a.plan_id,''),p.policy_version,a.sequence,a.active AND NOT w.stopped AND NOT p.paused AND (NOT p.require_plan OR a.plan_id IS NOT NULL)
        AND a.plan_id IS NOT DISTINCT FROM (SELECT id FROM plan_versions WHERE workspace_id=w.id AND project_id=p.id AND approved_at IS NOT NULL ORDER BY version DESC LIMIT 1),
        a.in_turn,c.id,c.operation,to_char(c.expires_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')
        FROM admission a JOIN devices d ON d.id=a.device_id AND d.workspace_id=a.workspace_id JOIN workspaces w ON w.id=a.workspace_id
        JOIN projects p ON p.id=a.project_id AND p.workspace_id=a.workspace_id JOIN device_projects m ON m.project_id=p.id AND m.device_id=d.id AND m.workspace_id=w.id
        LEFT JOIN LATERAL (SELECT id,operation,expires_at FROM controls WHERE workspace_id=w.id AND attempt_id=a.attempt_id AND project_id=p.id
            AND device_id=d.id AND subject_version=$8 AND status IN ('requested','received') AND expires_at>now() ORDER BY created_at DESC LIMIT 1) c ON true
        WHERE d.token_hash=$1 AND NOT d.revoked AND w.id=$2 AND d.id=$3 AND p.id=$4 AND a.attempt_id=$5 AND a.issue_number=$6 AND a.base_sha=$7",
        &[token,&a.workspace_id,&a.device_id,&a.project_id,&a.attempt_id,&a.issue_number.to_string(),&a.base_sha,&a.subject_version.to_string()])?.ok_or_else(Error::denied)?;
    if row.i64(2)? != a.sequence || row.bool(4)? != a.model_turn {
        return Err(Error::conflict(
            "Worker admission sequence or stage changed",
        ));
    }
    if let Some(control) = row.opt(5)? {
        return Ok(
            json!({"sequence":a.sequence,"validSeconds":0,"control":{"id":control,"workspaceId":a.workspace_id,
            "deviceId":a.device_id,"projectId":a.project_id,"attemptId":a.attempt_id,"operation":row.text(6)?,
            "subjectVersion":a.subject_version,"expiresAt":row.text(7)?}}),
        );
    }
    let plan = row.text(0)?;
    if let Some(s) = a.scope
        && (s.device_id != a.device_id
            || s.project_id != a.project_id
            || s.plan_id != plan
            || s.policy_version != row.i64(1)?)
    {
        return Err(Error::conflict("Local run authority changed"));
    }
    if !row.bool(3)? {
        return Err(Error::conflict("Worker authority suspended"));
    }
    if !plan::current_work(db, &a.workspace_id, &a.project_id, plan, a.issue_number)? {
        return Err(Error::conflict(plan::DRIFT));
    }
    Ok(json!({"sequence":a.sequence,"validSeconds":15}))
}
