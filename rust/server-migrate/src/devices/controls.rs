// SPDX-License-Identifier: MPL-2.0
use super::valid_key;
use crate::coord::{Db, Details, Error, Result};
use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Control {
    operation: String,
    version: i64,
    request_id: String,
}
#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Request {
    workspace: String,
    attempt: String,
    new_id: String,
    request: Control,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct Ack {
    id: String,
    status: String,
}

pub(super) fn request(db: &Db<'_>, input: Value) -> Result<Value> {
    let Request {
        workspace: ws,
        attempt,
        new_id,
        request: c,
    } = serde_json::from_value(input)?;
    if !valid_key(&c.request_id)
        || !matches!(c.operation.as_str(), "pause" | "stop")
        || c.version < 1
    {
        return Err(Error::bad(
            "Choose a control and the current attempt version",
        ));
    }
    // Serialize with credential revocation and sync using their device-first
    // lock order. A later version of a report must invalidate an old subject.
    let device = db.one(
        "SELECT d.id,d.revoked FROM devices d JOIN attempt_reports a
        ON (d.workspace_id,d.id)=(a.workspace_id,a.device_id)
        WHERE a.workspace_id=$1 AND a.id=$2 FOR SHARE OF d",
        &[&ws, &attempt],
    )?;
    let subject = db.one("SELECT project_id,state,version FROM attempt_reports WHERE workspace_id=$1 AND id=$2 FOR NO KEY UPDATE",
        &[&ws,&attempt])?;
    if let Some(prior) = db.optional(
        "SELECT id,status,operation,attempt_id,subject_version FROM controls
        WHERE workspace_id=$1 AND request_id=$2",
        &[&ws, &c.request_id],
    )? {
        if prior.text(2)? != c.operation || prior.text(3)? != attempt || prior.i64(4)? != c.version
        {
            return Err(Error::conflict("Request ID already used"));
        }
        return Ok(json!({"id":prior.text(0)?,"status":prior.text(1)?}));
    }
    if subject.i64(2)? != c.version
        || matches!(subject.text(1)?, "completed" | "failed" | "cancelled")
    {
        return Err(Error::conflict("Attempt changed or already ended"));
    }
    if device.bool(1)? {
        return Err(Error::conflict("The owning computer was revoked"));
    }
    db.exec(
        "UPDATE controls SET status='expired' WHERE workspace_id=$1 AND attempt_id=$2
        AND status IN ('requested','received') AND expires_at<=now()",
        &[&ws, &attempt],
    )?;
    if let Some(prior) = db.optional(
        "SELECT id,status,operation,subject_version FROM controls
        WHERE workspace_id=$1 AND attempt_id=$2 AND status IN ('requested','received') FOR UPDATE",
        &[&ws, &attempt],
    )? {
        if prior.text(2)? == c.operation && prior.i64(3)? == c.version {
            return Ok(json!({"id":prior.text(0)?,"status":prior.text(1)?}));
        }
        if prior.text(2)? == "stop" {
            return Err(Error::conflict("Stop already requested"));
        }
        db.exec(
            "UPDATE controls SET status='superseded' WHERE id=$1",
            &[prior.text(0)?],
        )?;
    }
    let project = subject.text(0)?;
    db.exec("INSERT INTO controls(id,workspace_id,project_id,attempt_id,device_id,operation,subject_version,status,request_id)
        VALUES ($1,$2,$3,$4,$5,$6,$7,'requested',$8)",
        &[&new_id,&ws,project,&attempt,device.text(0)?,&c.operation,&c.version.to_string(),&c.request_id])?;
    db.event(
        &ws,
        project,
        &format!("control.{}_requested", c.operation),
        &attempt,
        db.actor(),
        Details {
            reason: "Control requested against the current attempt",
            version: c.version,
            evidence: &new_id,
            ..Default::default()
        },
    )?;
    Ok(json!({"id":new_id,"status":"requested"}))
}

pub(super) fn deliver(db: &Db<'_>, ws: &str, device: &str, acks: Vec<Ack>) -> Result<Vec<Value>> {
    for ack in acks {
        if !valid_key(&ack.id)
            || !matches!(
                ack.status.as_str(),
                "received" | "completed" | "unavailable"
            )
        {
            return Err(Error::bad("Invalid command acknowledgment"));
        }
        let row = db
            .optional(
                "SELECT c.status,c.project_id,c.attempt_id FROM controls c JOIN device_projects m
            ON (m.workspace_id,m.device_id,m.project_id)=(c.workspace_id,c.device_id,c.project_id)
            WHERE c.id=$1 AND c.workspace_id=$2 AND c.device_id=$3 FOR UPDATE OF c",
                &[&ack.id, ws, device],
            )?
            .ok_or_else(Error::denied)?;
        let status = row.text(0)?;
        if status == ack.status || !matches!(status, "requested" | "received") {
            continue;
        }
        db.exec(
            "UPDATE controls SET status=$1 WHERE id=$2",
            &[&ack.status, &ack.id],
        )?;
        db.event(
            ws,
            row.text(1)?,
            &format!("control.{}", ack.status),
            row.text(2)?,
            ("device", device),
            Details::default(),
        )?;
    }
    db.rows("SELECT jsonb_build_object('id',c.id,'workspaceId',c.workspace_id,'projectId',c.project_id,
        'deviceId',c.device_id,'attemptId',c.attempt_id,'operation',c.operation,'subjectVersion',c.subject_version,'expiresAt',c.expires_at)
        FROM controls c JOIN device_projects m ON (m.workspace_id,m.device_id,m.project_id)=(c.workspace_id,c.device_id,c.project_id)
        WHERE c.workspace_id=$1 AND c.device_id=$2 AND c.status IN ('requested','received') AND c.expires_at>now()
        ORDER BY c.created_at,c.id LIMIT 64", &[ws, device])?
        .iter().map(|row| serde_json::from_str(row.text(0)?).map_err(|_| Error::internal())).collect()
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct WorkspaceControl {
    workspace: String,
    stopped: bool,
    version: i64,
    request_id: String,
}

pub(super) fn workspace_control(db: &Db<'_>, input: Value) -> Result<Value> {
    let b: WorkspaceControl = serde_json::from_value(input)?;
    if b.version < 1 || !valid_key(&b.request_id) {
        return Err(Error::bad(
            "Provide the operation, current version and request ID",
        ));
    }
    let w = db.one(
        "SELECT stopped,dispatch_version FROM workspaces WHERE id=$1 FOR NO KEY UPDATE",
        &[&b.workspace],
    )?;
    if let Some(prior) = db.optional("SELECT stopped,expected,result FROM workspace_controls WHERE workspace_id=$1 AND request_id=$2",
        &[&b.workspace,&b.request_id])? {
        if prior.bool(0)? != b.stopped || prior.i64(1)? != b.version { return Err(Error::conflict("Request already used for a different control")); }
        return Ok(json!({"stopped":b.stopped,"version":prior.i64(2)?}));
    }
    let mut version = w.i64(1)?;
    if version != b.version {
        return Err(Error::conflict(
            "Workspace controls changed. Review the current state.",
        ));
    }
    let stopped = b.stopped.to_string();
    if w.bool(0)? != b.stopped {
        version += 1;
        db.exec(
            "UPDATE workspaces SET stopped=$2,dispatch_version=$3 WHERE id=$1",
            &[&b.workspace, &stopped, &version.to_string()],
        )?;
        let kind = if b.stopped {
            db.exec(
                "UPDATE projects SET paused=true,version=version+1 WHERE workspace_id=$1",
                &[&b.workspace],
            )?;
            ensure_stops(db, &b.workspace, "")?;
            "workspace.emergency_stop"
        } else {
            "workspace.dispatch_unlocked"
        };
        db.event(
            &b.workspace,
            "",
            kind,
            &b.workspace,
            db.actor(),
            Details {
                reason: "Workspace dispatch control accepted",
                version,
                ..Default::default()
            },
        )?;
    }
    db.exec("INSERT INTO workspace_controls(workspace_id,request_id,stopped,expected,result) VALUES($1,$2,$3,$4,$5)",
        &[&b.workspace,&b.request_id,&stopped,&b.version.to_string(),&version.to_string()])?;
    Ok(json!({"stopped":b.stopped,"version":version}))
}

// The caller has verified the workspace-wide emergency stop. Bind each
// delivery to the current machine and latest demonstrated attempt version.
pub(super) fn ensure_stops(db: &Db<'_>, ws: &str, device: &str) -> Result<()> {
    // A machine can remain offline past the original delivery deadline.
    // Renew the same command only while its emergency and subject still match.
    db.exec("UPDATE controls c SET expires_at=now()+interval '24 hours'
        FROM attempt_reports a JOIN workspaces w ON w.id=a.workspace_id
        JOIN devices d ON (d.workspace_id,d.id)=(a.workspace_id,a.device_id)
        WHERE w.stopped AND NOT d.revoked AND a.state IN ('running','waiting')
        AND (c.workspace_id,c.attempt_id,c.device_id,c.subject_version)=(a.workspace_id,a.id,a.device_id,a.version)
        AND c.workspace_id=$1 AND ($2='' OR c.device_id=$2) AND c.operation='stop'
        AND c.request_id='emergency_'||a.id||'_'||a.version||'_'||w.dispatch_version
        AND c.status IN ('requested','received') AND c.expires_at<=now()", &[ws,device])?;
    db.exec("UPDATE controls c SET status=CASE WHEN c.expires_at<=now() THEN 'expired' ELSE 'superseded' END
        FROM attempt_reports a WHERE a.id=c.attempt_id
        AND a.workspace_id=c.workspace_id AND c.workspace_id=$1 AND ($2='' OR c.device_id=$2)
        AND c.status IN ('received','requested') AND (c.expires_at<=now() OR c.operation='pause'
        OR (c.status='requested' AND c.subject_version<>a.version))",
        &[ws,device])?;
    db.exec("INSERT INTO controls(id,workspace_id,project_id,attempt_id,device_id,operation,subject_version,status,request_id)
        SELECT gen_random_uuid()::text,a.workspace_id,a.project_id,a.id,a.device_id,'stop',a.version,'requested',
        'emergency_'||a.id||'_'||a.version||'_'||w.dispatch_version FROM attempt_reports a
        JOIN workspaces w ON w.id=a.workspace_id JOIN devices d ON (d.workspace_id,d.id)=(a.workspace_id,a.device_id)
        WHERE w.stopped AND a.workspace_id=$1 AND ($2='' OR a.device_id=$2) AND a.state IN('running','waiting')
        AND NOT d.revoked AND NOT EXISTS(SELECT FROM controls c WHERE c.workspace_id=a.workspace_id
        AND c.attempt_id=a.id AND c.status IN('received','requested'))
        ON CONFLICT DO NOTHING", &[ws,device])?;
    Ok(())
}
