// SPDX-License-Identifier: MPL-2.0
use crate::coord::{Db, Details, Error, Result};
use serde::Deserialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Share {
    credential_hash: String,
    attempt_id: String,
    version: i64,
    base_sha: String,
    candidate_sha: String,
    text: String,
    digest: String,
}

pub(super) fn share(db: &Db<'_>, input: Value) -> Result<Value> {
    let b: Share = serde_json::from_value(input)?;
    if b.text.is_empty()
        || b.text.len() > 24 << 10
        || hex::encode(Sha256::digest(b.text.as_bytes())) != b.digest
        || !crate::redact::shareable(&b.text)
    {
        return Err(Error::bad(
            "Diff is oversized or fails the digest or redaction check",
        ));
    }
    // Hold the device and attempt locks from authorization through the insert.
    let identity = db.optional("SELECT d.workspace_id,a.project_id,d.id FROM devices d
        JOIN attempt_reports a ON a.device_id=d.id AND a.workspace_id=d.workspace_id
        JOIN device_projects m ON (m.workspace_id,m.project_id,m.device_id)=(a.workspace_id,a.project_id,d.id)
        WHERE NOT d.revoked AND d.token_hash=$1 AND a.id=$2 AND a.version=$3 AND a.base_sha=$4
        AND a.candidate_sha=$5 AND a.state='completed' FOR SHARE OF d,a,m",
        &[&b.credential_hash,&b.attempt_id,&b.version.to_string(),&b.base_sha,&b.candidate_sha])?.ok_or_else(Error::denied)?;
    let (ws, project, device) = (identity.text(0)?, identity.text(1)?, identity.text(2)?);
    let added = db.exec("INSERT INTO shared_diffs(workspace_id,project_id,device_id,attempt_id,version,base_sha,candidate_sha,content,digest)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING",
        &[ws,project,device,&b.attempt_id,&b.version.to_string(),&b.base_sha,&b.candidate_sha,&b.text,&b.digest])?;
    if added != 0 {
        db.event(
            ws,
            project,
            "artifact.diff_shared",
            &b.attempt_id,
            ("device", device),
            Details {
                reason: "Owning device explicitly shared a validated diff",
                version: b.version,
                evidence: &b.digest,
                ..Default::default()
            },
        )?;
    } else if !db
        .one(
            "SELECT digest=$1 FROM shared_diffs WHERE workspace_id=$2 AND attempt_id=$3",
            &[&b.digest, ws, &b.attempt_id],
        )?
        .bool(0)?
    {
        return Err(Error::conflict("A different diff is already shared"));
    }
    Ok(json!({"digest":b.digest}))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Read {
    workspace: String,
    attempt: String,
}

pub(super) fn read(db: &Db<'_>, input: Value) -> Result<Value> {
    let b: Read = serde_json::from_value(input)?;
    let row = db.one("SELECT f.content,f.base_sha,f.candidate_sha,f.digest,to_json(f.created_at)#>>'{}'
        FROM shared_diffs f JOIN attempt_reports a ON (a.workspace_id,a.id)=(f.workspace_id,f.attempt_id)
        WHERE f.workspace_id=$1 AND f.attempt_id=$2 AND a.version=f.version AND a.candidate_sha=f.candidate_sha",
        &[&b.workspace,&b.attempt])?;
    Ok(
        json!({"text":row.text(0)?,"baseSha":row.text(1)?,"candidateSha":row.text(2)?,"digest":row.text(3)?,"sharedAt":row.text(4)?}),
    )
}
