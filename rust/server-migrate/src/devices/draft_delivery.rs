// SPDX-License-Identifier: MPL-2.0
use super::{lower_hex, valid_sha};
use crate::coord::{Db, Details, Error, Result};
use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Envelope {
    token_hash: String,
    request: Request,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Request {
    protocol: i64,
    operation: String,
    #[serde(default)]
    approval_version: i64,
    attempt_id: String,
    version: i64,
    base_sha: String,
    candidate_sha: String,
    target: String,
}

pub(super) fn handle(db: &Db<'_>, input: Value) -> Result<Value> {
    let Envelope {
        token_hash,
        request: b,
    } = serde_json::from_value(input)?;
    if b.protocol != 1
        || !matches!(b.operation.as_str(), "status" | "published")
        || b.approval_version < 0
        || b.operation == "published" && b.approval_version == 0
        || b.version < 1
        || b.attempt_id.len() != 32
        || !lower_hex(&b.attempt_id)
        || !valid_sha(&b.base_sha)
        || !valid_sha(&b.candidate_sha)
        || b.base_sha == b.candidate_sha
        || b.target.is_empty()
        || b.target.len() > 160
        || b.target.starts_with("bugsnitch/")
    {
        return Err(Error::bad("Invalid draft delivery subject"));
    }
    let identity = db.optional("SELECT d.workspace_id,p.id,d.id,p.policy_version FROM devices d
        JOIN attempt_reports a ON (a.workspace_id,a.device_id)=(d.workspace_id,d.id)
        JOIN device_projects dp ON (dp.workspace_id,dp.project_id,dp.device_id)=(a.workspace_id,a.project_id,d.id)
        JOIN projects p ON (p.workspace_id,p.id)=(a.workspace_id,a.project_id)
        JOIN workspaces w ON w.id=p.workspace_id
        WHERE NOT d.revoked AND d.token_hash=$1 AND a.id=$2 AND a.version=$3 AND a.base_sha=$4 AND a.candidate_sha=$5
        AND a.state='completed' AND a.phase='wrap_up' AND NOT w.stopped AND NOT p.paused AND p.allow_draft_pr
        FOR SHARE OF w,p,d,dp,a", &[&token_hash,&b.attempt_id,&b.version.to_string(),&b.base_sha,&b.candidate_sha])?.ok_or_else(Error::denied)?;
    let (ws, project, device) = (identity.text(0)?, identity.text(1)?, identity.text(2)?);
    let Some(row)=db.optional("SELECT x.status,x.reason,x.number,x.local_ship AND x.report_version=$4 AND x.policy_version=$5 AND x.base_sha=$6 AND x.candidate_sha=$7 AND x.target=$8 AND m.active,x.approval_version
        FROM draft_pr_intents x JOIN github_mappings m ON (m.workspace_id,m.project_id,m.installation_id,m.repository_id)=(x.workspace_id,x.project_id,x.installation_id,x.repository_id)
        WHERE x.workspace_id=$1 AND x.project_id=$2 AND x.attempt_id=$3 FOR UPDATE OF x FOR SHARE OF m",
        &[ws,project,&b.attempt_id,&b.version.to_string(),identity.text(3)?,&b.base_sha,&b.candidate_sha,&b.target])? else {
        return if b.operation=="status"{Ok(json!({"status":"awaiting_approval"}))}else{Err(Error::denied())};
    };
    if !row.bool(3)? {
        return Err(Error::conflict(
            "Draft approval does not match this delivery",
        ));
    }
    let generation = row.i64(4)?;
    if b.approval_version != 0 && b.approval_version != generation {
        return Err(Error::conflict(
            "Owner draft approval changed. Review the current approval.",
        ));
    }
    if row.text(0)? == "awaiting_publication" && b.operation == "published" {
        db.exec("UPDATE draft_pr_intents SET status='queued',next_run=now(),updated_at=now() WHERE workspace_id=$1 AND attempt_id=$2",&[ws,&b.attempt_id])?;
        db.event(
            ws,
            project,
            "draft.publication_confirmed",
            &b.attempt_id,
            ("device", device),
            Details {
                reason: "Originating device confirmed publication of the owner-approved candidate",
                version: b.version,
                evidence: &b.candidate_sha,
                ..Default::default()
            },
        )?;
        return Ok(json!({"status":"queued","approvalVersion":generation}));
    }
    Ok(
        json!({"status":row.text(0)?,"approvalVersion":generation,"reason":row.text(1)?,"number":row.opt(2)?.map(|_|row.i64(2)).transpose()?}),
    )
}
