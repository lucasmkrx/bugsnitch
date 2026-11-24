// SPDX-License-Identifier: MPL-2.0
use crate::Failure;
use rusqlite::{Connection, OptionalExtension, TransactionBehavior, params};
use serde::Deserialize;

#[derive(Deserialize)]
#[serde(tag = "action", rename_all = "snake_case", deny_unknown_fields)]
pub(super) enum Control {
    Apply {
        id: String,
        command: String,
        op: Operation,
        version: i64,
        at: String,
    },
    Finish {
        id: String,
        command: String,
        version: i64,
    },
    Pending {
        id: String,
    },
    Settle {
        id: String,
        version: i64,
        data: String,
    },
}

#[derive(Deserialize, Clone, Copy)]
#[serde(rename_all = "lowercase")]
pub(super) enum Operation {
    Pause,
    Stop,
}
impl Operation {
    fn name(self) -> &'static str {
        match self {
            Self::Pause => "pause",
            Self::Stop => "stop",
        }
    }
    fn state(self) -> &'static str {
        match self {
            Self::Pause => "waiting",
            Self::Stop => "cancelled",
        }
    }
}

fn pending(db: &Connection, id: &str) -> rusqlite::Result<Option<String>> {
    db.query_row(
        "SELECT op FROM journal_controls WHERE attempt_id=?1 AND status='received'
        ORDER BY CASE op WHEN 'stop' THEN 0 ELSE 1 END LIMIT 1",
        [id],
        |row| row.get(0),
    )
    .optional()
}

fn receipt(
    db: &Connection,
    id: &str,
    command: &str,
    op: &str,
    version: i64,
) -> rusqlite::Result<String> {
    Ok(db.query_row("SELECT status FROM journal_controls WHERE attempt_id=?1 AND command_id=?2 AND op=?3 AND subject_version=?4",
        params![id, command, op, version], |row| row.get(0)).optional()?.unwrap_or_else(|| "unavailable".into()))
}

fn valid_subject(command: &str, version: i64) -> bool {
    (1..=128).contains(&command.len())
        && command
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || c == b'_' || c == b'-')
        && version > 0
}

pub(super) fn execute(db: &mut Connection, control: Control) -> Result<Vec<u8>, Failure> {
    let id = match &control {
        Control::Apply { id, .. }
        | Control::Finish { id, .. }
        | Control::Settle { id, .. }
        | Control::Pending { id } => id,
    };
    if id.len() != 32
        || !id
            .bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
    {
        return Err(Failure::Protocol);
    }
    if let Control::Pending { id } = control {
        return Ok(pending(db, &id)?.unwrap_or_default().into_bytes());
    }
    let tx = db.transaction_with_behavior(TransactionBehavior::Immediate)?;
    let result = match control {
        Control::Apply {
            id,
            command,
            op,
            version,
            at,
        } => {
            if !valid_subject(&command, version) || at.len() > 64 {
                return Err(Failure::Protocol);
            }
            tx.execute("INSERT OR IGNORE INTO journal_controls(command_id,attempt_id,op,subject_version,status)
                SELECT ?1,id,?2,?3,CASE WHEN version<>?3 THEN 'unavailable'
                WHEN json_extract(data,'$.state')='running' THEN 'received'
                WHEN json_extract(data,'$.state')='waiting' THEN 'apply_waiting'
                ELSE 'unavailable' END FROM journal_attempts WHERE id=?4",
                params![command,op.name(),version,id])?;
            let mut status = receipt(&tx, &id, &command, op.name(), version)?;
            if status == "apply_waiting" {
                tx.execute("UPDATE journal_attempts SET version=version+1,
                    data=json_set(data,'$.version',version+1,'$.state',?1,'$.reasonCode',
                    CASE ?1 WHEN 'waiting' THEN 'owner_paused' ELSE '' END,'$.Message','Owner control applied','$.UpdatedAt',?2)
                    WHERE id=?3 AND version=?4", params![op.state(),at,id,version])?;
                status = match op {
                    Operation::Stop => "finish_pending",
                    Operation::Pause => "completed",
                }
                .into();
                tx.execute(
                    "UPDATE journal_controls SET status=?1 WHERE command_id=?2",
                    params![status, command],
                )?;
            }
            status
        }
        Control::Finish {
            id,
            command,
            version,
        } => {
            if !valid_subject(&command, version) {
                return Err(Failure::Protocol);
            }
            tx.execute("UPDATE journal_controls SET status='completed' WHERE command_id=?1 AND attempt_id=?2
                AND op='stop' AND subject_version=?3 AND status='finish_pending'", params![command,id,version])?;
            receipt(&tx, &id, &command, "stop", version)?
        }
        Control::Settle { id, version, data } => {
            // The owning execution has quiesced. Complete receipts only if its
            // exact revision wins the compare-and-swap in this transaction.
            let op = pending(&tx, &id)?;
            let changed = tx.execute("UPDATE journal_attempts SET version=version+1,
                data=CASE WHEN ?1 IS NULL THEN ?2 ELSE json_set(?2,'$.state',CASE ?1 WHEN 'stop' THEN 'cancelled'
                ELSE 'waiting' END,'$.reasonCode',CASE ?1 WHEN 'pause' THEN 'owner_paused' ELSE '' END,'$.Message','Owner control applied') END
                WHERE id=?3 AND version=?4 AND json_extract(?2,'$.id')=id AND json_extract(?2,'$.version')=version+1",
                params![op,data,id,version])?;
            if changed == 0 {
                "stale".into()
            } else {
                tx.execute("UPDATE journal_controls SET status='completed' WHERE attempt_id=?1 AND status='received'", [&id])?;
                op.unwrap_or_default()
            }
        }
        Control::Pending { .. } => unreachable!(),
    };
    tx.commit()?;
    Ok(result.into_bytes())
}
