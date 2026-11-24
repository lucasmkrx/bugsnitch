// SPDX-License-Identifier: MPL-2.0
//! Device operations execute inside the coordinator's single PostgreSQL transaction.
//! Only hashes of pairing codes and bearer credentials cross the local protocol.
mod artifacts;
mod controls;
mod draft_delivery;
mod metering;
mod sync;

use crate::coord::{Db, Details, Error, Result};
use serde::Deserialize;
use serde_json::{Value, json};
use std::collections::HashSet;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Pairing {
    workspace: String,
    name: String,
    projects: Vec<String>,
    code_hash: String,
    expires_at: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Redeem {
    code_hash: String,
    credential_hash: String,
    platform: String,
    new_id: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Revoke {
    workspace: String,
    device: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Rotate {
    current_hash: String,
    next_hash: String,
}

pub fn handle(db: &Db<'_>, op: &str, input: Value) -> Result<Value> {
    match op {
        "device.draft_delivery" => draft_delivery::handle(db, input),
        "device.share_diff" => artifacts::share(db, input),
        "device.read_diff" => artifacts::read(db, input),
        "device.pairing" => pairing(db, serde_json::from_value(input)?),
        "device.redeem" => redeem(db, serde_json::from_value(input)?),
        "device.revoke" => revoke(db, serde_json::from_value(input)?),
        "device.rotate" => rotate(db, serde_json::from_value(input)?),
        "device.sync" => sync::handle(db, input),
        "device.control" => controls::request(db, input),
        "device.workspace_control" => controls::workspace_control(db, input),
        _ => Err(Error::bad("Unknown device operation")),
    }
}

fn pairing(db: &Db<'_>, b: Pairing) -> Result<Value> {
    if b.name.trim().is_empty() || b.name.len() > 100 || !(1..=40).contains(&b.projects.len()) {
        return Err(Error::bad("Choose a device name and one to forty projects"));
    }
    if b.projects.iter().collect::<HashSet<_>>().len() != b.projects.len() {
        return Err(Error::bad("Duplicate project"));
    }
    let projects = serde_json::to_string(&b.projects)?;
    let count = db
        .one(
            "SELECT count(*) FROM projects WHERE workspace_id=$1 AND id IN
        (SELECT jsonb_array_elements_text($2::jsonb))",
            &[&b.workspace, &projects],
        )?
        .i64(0)?;
    if count != b.projects.len() as i64 {
        return Err(Error::missing());
    }
    db.exec(
        "INSERT INTO pairings(code_hash,workspace_id,name,project_ids,expires_at)
        VALUES($1,$2,$3,$4::jsonb,$5::timestamptz)",
        &[
            &b.code_hash,
            &b.workspace,
            &b.name,
            &projects,
            &b.expires_at,
        ],
    )?;
    for project in b.projects {
        db.event(
            &b.workspace,
            &project,
            "device.enrollment_authorized",
            &project,
            db.actor(),
            Details {
                reason: "Short-lived device enrollment authorized for this project",
                ..Default::default()
            },
        )?;
    }
    Ok(Value::Null)
}

fn projects(db: &Db<'_>, workspace: &str, device: &str) -> Result<Vec<Value>> {
    db.rows(
        "SELECT p.id,p.repository,(p.paused OR w.stopped),p.policy_version,coalesce((SELECT v.id FROM plan_versions v
          WHERE v.workspace_id=p.workspace_id AND v.project_id=p.id AND v.approved_at IS NOT NULL ORDER BY v.version DESC LIMIT 1),'')
        FROM projects p JOIN workspaces w ON w.id=p.workspace_id JOIN device_projects m
        ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id)
        WHERE m.workspace_id=$1 AND m.device_id=$2 ORDER BY p.id",
        &[workspace, device],
    )?
    .iter()
    .map(|r| Ok(json!({"id":r.text(0)?, "repository":r.text(1)?, "paused":r.bool(2)?,"policyVersion":r.i64(3)?,"planId":r.text(4)?})))
    .collect()
}

fn redeem(db: &Db<'_>, b: Redeem) -> Result<Value> {
    let pairing = db
        .optional(
            "SELECT workspace_id,name,project_ids,device_id FROM pairings
        WHERE code_hash=$1 AND expires_at>now() FOR UPDATE",
            &[&b.code_hash],
        )?
        .ok_or_else(Error::denied)?;
    let workspace = pairing.text(0)?;
    let device = if let Some(id) = pairing.opt(3)? {
        // Lost response: the original recipient alone can replay, including
        // when device revocation or rotation races the enrollment response.
        db.optional(
            "SELECT id FROM devices WHERE id=$1 AND workspace_id=$2 AND token_hash=$3
            AND NOT revoked FOR SHARE",
            &[id, workspace, &b.credential_hash],
        )?
        .ok_or_else(Error::denied)?;
        id
    } else {
        db.exec(
            "INSERT INTO devices(id,workspace_id,name,platform,token_hash) VALUES ($1,$2,$3,$4,$5)",
            &[
                &b.new_id,
                workspace,
                pairing.text(1)?,
                &b.platform,
                &b.credential_hash,
            ],
        )?;
        db.exec(
            "INSERT INTO device_projects(workspace_id,device_id,project_id)
            SELECT $1,$2,jsonb_array_elements_text($3::jsonb)",
            &[workspace, &b.new_id, pairing.text(2)?],
        )?;
        db.exec(
            "UPDATE pairings SET device_id=$2 WHERE code_hash=$1",
            &[&b.code_hash, &b.new_id],
        )?;
        db.event(
            workspace,
            "",
            "device.paired",
            &b.new_id,
            ("device", &b.new_id),
            Details {
                reason: "Owner-issued pairing code redeemed by this device",
                ..Default::default()
            },
        )?;
        &b.new_id
    };
    Ok(json!({"workspaceId":workspace,"deviceId":device,"projects":projects(db,workspace,device)?}))
}

fn revoke(db: &Db<'_>, b: Revoke) -> Result<Value> {
    let revoked = db
        .one(
            "SELECT revoked FROM devices WHERE workspace_id=$1 AND id=$2 FOR UPDATE",
            &[&b.workspace, &b.device],
        )?
        .bool(0)?;
    if !revoked {
        db.exec(
            "UPDATE devices SET revoked=true,token_hash=NULL WHERE workspace_id=$1 AND id=$2",
            &[&b.workspace, &b.device],
        )?;
        db.event(
            &b.workspace,
            "",
            "device.revoked",
            &b.device,
            db.actor(),
            Details {
                reason: "Owner revoked the device credential",
                ..Default::default()
            },
        )?;
    }
    Ok(Value::Null)
}

fn rotate(db: &Db<'_>, b: Rotate) -> Result<Value> {
    if b.current_hash == b.next_hash {
        return Err(Error::bad("Provide a fresh device credential"));
    }
    let matches = db.rows("SELECT workspace_id,id,token_hash FROM devices WHERE NOT revoked
        AND (token_hash=$1 OR (previous_hash=$1 AND token_hash=$2 AND rotated_at>now()-interval '1 day'))
        FOR UPDATE", &[&b.current_hash,&b.next_hash])?;
    let [row] = matches.as_slice() else {
        return Err(Error::denied());
    };
    let (workspace, id) = (row.text(0)?, row.text(1)?);
    if row.text(2)? != b.next_hash {
        db.exec("UPDATE devices SET previous_hash=token_hash,token_hash=$1,rotated_at=now() WHERE workspace_id=$2 AND id=$3",
            &[&b.next_hash,workspace,id])?;
        db.event(
            workspace,
            "",
            "device.rotated",
            id,
            ("device", id),
            Details {
                reason: "Device authenticated with its previous credential",
                ..Default::default()
            },
        )?;
    }
    Ok(json!({"deviceId":id}))
}

pub(crate) fn valid_key(id: &str) -> bool {
    (8..=100).contains(&id.len())
        && id
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, b'-' | b'_'))
}

pub(crate) fn lower_hex(value: &str) -> bool {
    value
        .bytes()
        .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

pub(crate) fn valid_sha(value: &str) -> bool {
    matches!(value.len(), 40 | 64) && lower_hex(value)
}
