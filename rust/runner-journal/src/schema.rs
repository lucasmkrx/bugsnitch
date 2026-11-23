// SPDX-License-Identifier: MPL-2.0
use crate::Failure;
use rusqlite::{Connection, TransactionBehavior};

const DEVICE_SCHEMA: &str = "
    CREATE TABLE IF NOT EXISTS profile(id INTEGER PRIMARY KEY CHECK(id=1),data TEXT NOT NULL);
    CREATE TABLE IF NOT EXISTS mappings(id TEXT PRIMARY KEY,path TEXT NOT NULL);
    CREATE TABLE IF NOT EXISTS authority(attempt TEXT PRIMARY KEY,device TEXT NOT NULL,
        acked INTEGER NOT NULL DEFAULT 0,pending TEXT NOT NULL DEFAULT '');";

const RUNNER_CAPACITY: &str = "
    CREATE TABLE IF NOT EXISTS runner_capacity(
        id INTEGER PRIMARY KEY CHECK(id=1),
        ticket_limit INTEGER NOT NULL DEFAULT 2 CHECK(ticket_limit BETWEEN 1 AND 32),
        turn_limit INTEGER NOT NULL DEFAULT 2 CHECK(turn_limit BETWEEN 1 AND 32)
    ) STRICT;
    INSERT OR IGNORE INTO runner_capacity(id) VALUES(1);";

fn version(conn: &Connection) -> rusqlite::Result<i64> {
    conn.pragma_query_value(None, "user_version", |row| row.get(0))
}

pub(super) fn prepare(conn: &mut Connection, runner: bool) -> Result<(), Failure> {
    if !runner {
        return Ok(conn.execute_batch(DEVICE_SCHEMA)?);
    }
    // Capacity is an additive extension to v2. An upgraded journal remains
    // readable by the previous helper. Ordinary access only needs a read.
    let (current, has_capacity): (i64, bool) = conn
        .prepare_cached(
            "SELECT user_version, EXISTS(SELECT 1 FROM sqlite_schema
             WHERE type='table' AND name='runner_capacity') FROM pragma_user_version",
        )?
        .query_row([], |row| Ok((row.get(0)?, row.get(1)?)))?;
    match current {
        2 if has_capacity => return Ok(()),
        0..=2 => {}
        _ => return Err(Failure::Schema),
    }
    let tx = conn.transaction_with_behavior(TransactionBehavior::Immediate)?;
    // Another process may have finished migration while we waited for the lock.
    match version(&tx)? {
        0 | 1 => tx.execute_batch(include_str!("upgrade_v2.sql"))?,
        2 => {}
        _ => return Err(Failure::Schema),
    }
    tx.execute_batch(RUNNER_CAPACITY)?;
    Ok(tx.commit()?)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn capacity_is_added_to_existing_v2_journals_once() {
        let mut db = Connection::open_in_memory().unwrap();
        db.execute_batch(include_str!("upgrade_v2.sql")).unwrap();
        db.execute(
            "INSERT INTO journal_attempts VALUES('existing','personal','a/b',1,7,'{}',1)",
            [],
        )
        .unwrap();
        prepare(&mut db, true).unwrap();
        let limits = |db: &Connection| {
            db.query_row(
                "SELECT ticket_limit, turn_limit FROM runner_capacity WHERE id=1",
                [],
                |row| Ok((row.get::<_, i64>(0)?, row.get::<_, i64>(1)?)),
            )
            .unwrap()
        };
        assert_eq!(limits(&db), (2, 2));
        assert!(
            db.execute("UPDATE runner_capacity SET turn_limit=33", [])
                .is_err()
        );
        db.execute("UPDATE runner_capacity SET ticket_limit=4", [])
            .unwrap();
        db.pragma_update(None, "query_only", true).unwrap();
        prepare(&mut db, true).unwrap();
        assert_eq!(limits(&db), (4, 2));
        let revision: i64 = db
            .query_row(
                "SELECT version FROM journal_attempts WHERE id='existing'",
                [],
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(revision, 7);
        assert_eq!(version(&db).unwrap(), 2);
    }

    #[test]
    fn a_newer_runner_schema_is_preserved() {
        let mut db = Connection::open_in_memory().unwrap();
        db.pragma_update(None, "user_version", 3).unwrap();
        assert!(matches!(prepare(&mut db, true), Err(Failure::Schema)));
        assert_eq!(version(&db).unwrap(), 3);
    }
}
