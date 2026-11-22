// SPDX-License-Identifier: MPL-2.0
use crate::{Failure, MAX_FRAME};
use rusqlite::{Connection, Params, Statement, params, types::ValueRef};
use serde::Deserialize;
use serde_json::value::RawValue;

#[derive(Deserialize)]
#[serde(rename_all = "snake_case", deny_unknown_fields)]
pub(super) enum Operation {
    Get {
        id: String,
    },
    Summaries {},
    Save {
        id: String,
        version: i64,
        data: Box<RawValue>,
        claim: bool,
    },
    SyncAuthority {
        device: String,
        workspace: String,
    },
}

const SUMMARIES: &str = "SELECT json_group_array(json_object(
    'id',id,'workspace',workspace,'repository',repo,'issueNumber',issue,
    'issueUpdatedAt',json_extract(data,'$.Issue.updatedAt'),'reasonCode',json_extract(data,'$.reasonCode'),
    'phase',json_extract(data,'$.phase'),'state',json_extract(data,'$.state'),
    'baseSha',json_extract(data,'$.baseSha'),'candidateSha',json_extract(data,'$.candidateSha'),
    'version',version,'evidence',json_extract(data,'$.evidence')))
    FROM (SELECT * FROM journal_attempts ORDER BY rowid)";

// Keep the transport limit before copying a result out of SQLite. An absent
// attempt (or a device unpaired during sync) returns an empty frame.
fn first_json(stmt: &mut Statement<'_>, args: impl Params) -> Result<Vec<u8>, Failure> {
    let mut rows = stmt.query(args)?;
    let Some(row) = rows.next()? else {
        return Ok(Vec::new());
    };
    let bytes = match row.get_ref(0)? {
        ValueRef::Text(bytes) | ValueRef::Blob(bytes) => bytes,
        _ => return Err(Failure::Database),
    };
    if bytes.len() > MAX_FRAME {
        return Err(Failure::TooLarge);
    }
    Ok(bytes.to_vec())
}

pub(super) fn execute(db: &Connection, operation: Operation) -> Result<Vec<u8>, Failure> {
    match operation {
        Operation::Get { id } => {
            let mut stmt = db.prepare_cached("SELECT data FROM journal_attempts WHERE id=?1")?;
            first_json(&mut stmt, [id])
        }
        Operation::Summaries {} => {
            let mut stmt = db.prepare_cached(SUMMARIES)?;
            first_json(&mut stmt, [])
        }
        Operation::Save {
            id,
            version,
            data,
            claim,
        } => {
            // Both the ordinary checkpoint and a capacity claim compare the
            // previously observed revision inside one atomic SQLite update.
            let sql = if claim {
                "UPDATE journal_attempts AS target SET version=version+1,data=?1
                 WHERE id=?2 AND version=?3 AND json_extract(data,'$.state')='waiting' AND json_extract(?1,'$.state')='running'
                 AND (SELECT count(*) FROM journal_attempts AS active WHERE active.workspace=target.workspace
                      AND json_extract(active.data,'$.state')='running')<2"
            } else {
                "UPDATE journal_attempts SET version=version+1,data=?1 WHERE id=?2 AND version=?3"
            };
            let changed = db
                .prepare_cached(sql)?
                .execute(params![data.get(), id, version])?;
            Ok(if changed == 1 {
                b"1".to_vec()
            } else {
                b"0".to_vec()
            })
        }
        Operation::SyncAuthority { .. } => Err(Failure::Protocol),
    }
}

pub(super) fn sync_authority(
    db: &Connection,
    device: &str,
    workspace: &str,
) -> Result<Vec<u8>, Failure> {
    // A single read snapshot. Return IDs only. Credential data, pending
    // admissions, and local checkout paths stay out of the sync projection.
    let mut statement = db.prepare(
        "SELECT json_object('projects',json((SELECT json_group_object(id,1) FROM mappings)),
           'attempts',json((SELECT json_group_object(attempt,1) FROM authority WHERE device=?1 AND acked>0)))
         FROM profile WHERE id=1 AND json_extract(data,'$.Enrollment.deviceId')=?1 AND json_extract(data,'$.Enrollment.workspaceId')=?2",
    )?;
    first_json(&mut statement, [device, workspace])
}
