// SPDX-License-Identifier: MPL-2.0
use crate::Failure;
use rusqlite::{Connection, TransactionBehavior};

const DEVICE_SCHEMA: &str = "
    CREATE TABLE IF NOT EXISTS profile(id INTEGER PRIMARY KEY CHECK(id=1),data TEXT NOT NULL);
    CREATE TABLE IF NOT EXISTS mappings(id TEXT PRIMARY KEY,path TEXT NOT NULL);
    CREATE TABLE IF NOT EXISTS authority(attempt TEXT PRIMARY KEY,device TEXT NOT NULL,
        acked INTEGER NOT NULL DEFAULT 0,pending TEXT NOT NULL DEFAULT '');";

fn version(conn: &Connection) -> rusqlite::Result<i64> {
    conn.pragma_query_value(None, "user_version", |row| row.get(0))
}

pub(super) fn prepare(conn: &mut Connection, runner: bool) -> Result<(), Failure> {
    if !runner {
        return Ok(conn.execute_batch(DEVICE_SCHEMA)?);
    }
    match version(conn)? {
        2 => return Ok(()),
        0 | 1 => {}
        _ => return Err(Failure::Schema),
    }
    let tx = conn.transaction_with_behavior(TransactionBehavior::Immediate)?;
    // Another process may have finished migration while we waited for the lock.
    match version(&tx)? {
        0 | 1 => tx.execute_batch(include_str!("upgrade_v2.sql"))?,
        2 => {}
        _ => return Err(Failure::Schema),
    }
    Ok(tx.commit()?)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_newer_runner_schema_is_preserved() {
        let mut db = Connection::open_in_memory().unwrap();
        db.pragma_update(None, "user_version", 3).unwrap();
        assert!(matches!(prepare(&mut db, true), Err(Failure::Schema)));
        assert_eq!(version(&db).unwrap(), 3);
    }
}
