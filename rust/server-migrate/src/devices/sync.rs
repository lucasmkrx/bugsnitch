// SPDX-License-Identifier: MPL-2.0
use super::{controls, projects};
use crate::coord::{Db, Details, Error, Result};
use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Request {
    credential_hash: String,
    sync: Sync,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Sync {
    protocol: i64,
    attempts: Option<Vec<Report>>,
    acks: Option<Vec<controls::Ack>>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Report {
    id: String,
    project_id: String,
    issue_number: i64,
    base_sha: String,
    candidate_sha: String,
    phase: String,
    state: String,
    version: i64,
    #[serde(default)]
    evidence: Vec<Value>,
    #[serde(default)]
    issue_updated_at: String,
    #[serde(default)]
    reason_code: String,
}

pub(super) fn handle(db: &Db<'_>, input: Value) -> Result<Value> {
    let Request {
        credential_hash,
        sync,
    } = serde_json::from_value(input)?;
    let reports = sync.attempts.unwrap_or_default();
    let acks = sync.acks.unwrap_or_default();
    if sync.protocol != 1 || reports.len() > 64 || acks.len() > 64 {
        return Err(Error::bad("Invalid device sync envelope"));
    }
    let identity = db.optional("WITH prev AS MATERIALIZED (
        SELECT id,last_contact,diagnostics_since FROM devices WHERE token_hash=$1 AND NOT revoked FOR NO KEY UPDATE
        ), observed AS MATERIALIZED (SELECT *,clock_timestamp() AS contacted FROM prev)
        UPDATE devices d SET last_contact=o.contacted,diagnostics_since=coalesce(o.diagnostics_since,o.contacted)
        FROM observed o WHERE d.id=o.id RETURNING d.id,d.workspace_id,
        o.diagnostics_since IS NOT NULL AND o.contacted-o.last_contact>interval '2 minutes'",
        &[&credential_hash])?.ok_or_else(Error::denied)?;
    let device = identity.text(0)?;
    let workspace = identity.text(1)?;
    if identity.bool(2)? {
        db.event(
            workspace,
            "",
            "device.reconnected",
            device,
            ("device", device),
            Details {
                reason: "Authenticated sync resumed after more than two minutes without contact",
                ..Default::default()
            },
        )?;
    }
    let assigned = projects(db, workspace, device)?;
    for report in reports {
        if !assigned
            .iter()
            .any(|project| project["id"].as_str() == Some(&report.project_id))
        {
            return Err(Error::denied());
        }
        save_report(db, workspace, device, &report)?;
    }
    if db
        .one("SELECT stopped FROM workspaces WHERE id=$1", &[workspace])?
        .bool(0)?
    {
        controls::ensure_stops(db, workspace, device)?;
    }
    let commands = controls::deliver(db, workspace, device, acks)?;
    Ok(
        json!({"workspaceId":workspace,"deviceId":device,"stageTimings":true,"projects":assigned,"commands":commands}),
    )
}

fn save_report(db: &Db<'_>, workspace: &str, device: &str, a: &Report) -> Result<()> {
    let number = a.issue_number.to_string();
    let revision = a.version.to_string();
    if db
        .optional(
            "SELECT 1 FROM admission WHERE workspace_id=$1 AND attempt_id=$2 AND device_id=$3
        AND project_id=$4 AND issue_number=$5 AND base_sha=$6",
            &[
                workspace,
                &a.id,
                device,
                &a.project_id,
                &number,
                &a.base_sha,
            ],
        )?
        .is_none()
    {
        return Err(Error::denied());
    }

    // A reconciled snapshot can skip versions, but cannot move the issue,
    // machine, base or finished attempt. An older runner may supply the
    // issue revision later on an otherwise identical, already recorded report.
    let evidence = serde_json::to_string(&a.evidence)?;
    let updated = db.exec("INSERT INTO attempt_reports
        (id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version,evidence,issue_updated_at,reason_code)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13)
        ON CONFLICT (workspace_id,id) DO UPDATE SET reason_code=EXCLUDED.reason_code,
        issue_updated_at=coalesce(nullif(EXCLUDED.issue_updated_at,''),attempt_reports.issue_updated_at),
        evidence=EXCLUDED.evidence,candidate_sha=EXCLUDED.candidate_sha,phase=EXCLUDED.phase,state=EXCLUDED.state,
        version=EXCLUDED.version,updated_at=now()
        WHERE (attempt_reports.device_id,attempt_reports.project_id,attempt_reports.issue_number,attempt_reports.base_sha)
        = (EXCLUDED.device_id,EXCLUDED.project_id,EXCLUDED.issue_number,EXCLUDED.base_sha)
        AND (attempt_reports.issue_updated_at='' OR EXCLUDED.issue_updated_at='' OR attempt_reports.issue_updated_at=EXCLUDED.issue_updated_at)
        AND ((attempt_reports.version<EXCLUDED.version AND attempt_reports.state NOT IN ('completed','failed','cancelled')
        AND (EXCLUDED.phase<>'plan' OR attempt_reports.phase='plan') AND (EXCLUDED.candidate_sha<>'' OR attempt_reports.candidate_sha=''))
        OR (attempt_reports.issue_updated_at='' AND EXCLUDED.issue_updated_at<>''
        AND (attempt_reports.version,attempt_reports.state,attempt_reports.phase,attempt_reports.candidate_sha,attempt_reports.evidence,attempt_reports.reason_code)
          = (EXCLUDED.version,EXCLUDED.state,EXCLUDED.phase,EXCLUDED.candidate_sha,EXCLUDED.evidence,EXCLUDED.reason_code)))",
        &[&a.id,workspace,&a.project_id,device,&number,&a.base_sha,&a.candidate_sha,&a.phase,&a.state,&revision,&evidence,&a.issue_updated_at,&a.reason_code])?;
    if updated == 1 {
        let sha = if a.candidate_sha.is_empty() {
            &a.base_sha
        } else {
            &a.candidate_sha
        };
        db.event(
            workspace,
            &a.project_id,
            "attempt.reported",
            &a.id,
            ("device", device),
            Details {
                reason: "Owning device synchronized a journaled attempt",
                version: a.version,
                evidence: sha,
                ..Default::default()
            },
        )?;
    } else {
        let permitted = db.one("SELECT (device_id,project_id,issue_number,base_sha)=($3,$4,$5::int,$6)
            AND (version>$7::bigint OR (version=$7 AND candidate_sha=$8 AND phase=$9 AND state=$10 AND evidence=$11::jsonb
            AND reason_code=$13 AND ($12='' OR issue_updated_at=$12))) FROM attempt_reports WHERE workspace_id=$1 AND id=$2",
            &[workspace,&a.id,device,&a.project_id,&number,&a.base_sha,&revision,&a.candidate_sha,&a.phase,&a.state,&evidence,&a.issue_updated_at,&a.reason_code])?.bool(0)?;
        if !permitted {
            return Err(Error::conflict(
                "Attempt identity or version conflicts with the stored report",
            ));
        }
    }
    Ok(())
}
