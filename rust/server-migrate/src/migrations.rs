// SPDX-License-Identifier: MPL-2.0
use crate::config;
use libpq::{Connection, Format, PQResult, Status};
use std::env;

const MIGRATIONS: &[(&str, &[u8])] = include!(concat!(env!("OUT_DIR"), "/migrations.rs"));
const LEDGER: &str = "CREATE TABLE IF NOT EXISTS bugsnitch_migrations(name text PRIMARY KEY, digest text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())";

fn checked(result: PQResult) -> Result<PQResult, &'static str> {
    match result.status() {
        Status::TuplesOk | Status::CommandOk => Ok(result),
        _ => Err("PostgreSQL rejected a migration operation"),
    }
}

// Supply exact bytes with explicitly typed SQL parameters. In particular,
// DSN-provided schema names never become SQL identifiers or query fragments.
fn query(db: &Connection, sql: &str, params: &[&[u8]]) -> Result<PQResult, &'static str> {
    let values: Vec<_> = params.iter().copied().map(Some).collect();
    let formats = vec![Format::Binary; values.len()];
    checked(db.exec_params(sql, &[], &values, &formats, Format::Text))
}

pub fn run(timeout: u64) -> Result<(), &'static str> {
    let dsn = env::var("DATABASE_URL").map_err(|_| "Set DATABASE_URL for the migrator")?;
    let cfg = config::split(&dsn).map_err(|_| "Invalid PostgreSQL connection settings")?;
    let db = Connection::new(&cfg.native).map_err(|_| "PostgreSQL connection failed")?;
    checked(db.exec("BEGIN; SET LOCAL client_min_messages TO ERROR"))?;
    // Dropping the connection rolls back all DDL and releases the transaction
    // lock on every error path. The server timeout also covers lost clients.
    query(
        &db,
        "SELECT pg_catalog.set_config('statement_timeout',$1::text,true)",
        &[timeout.to_string().as_bytes()],
    )?;
    if let Some(schema) = cfg.search_path {
        query(
            &db,
            "SELECT pg_catalog.set_config('search_path',$1::text,true)",
            &[schema.as_bytes()],
        )?;
    }
    checked(db.exec("SELECT pg_catalog.pg_advisory_xact_lock(628719844)"))?;
    checked(db.exec(LEDGER))?;
    for &(name, sql) in MIGRATIONS {
        let args = [name.as_bytes(), sql];
        let prior = query(
            &db,
            "SELECT digest = pg_catalog.encode(pg_catalog.sha256($2::bytea),'hex') FROM bugsnitch_migrations WHERE name=$1::text",
            &args,
        )?;
        if prior.ntuples() != 0 {
            if prior.value(0, 0) != Some(b"t") {
                return Err("Applied migration checksum differs from the packaged SQL");
            }
            continue;
        }
        let script = std::str::from_utf8(sql).map_err(|_| "Invalid packaged migration")?;
        checked(db.exec(script)).map_err(|_| name)?;
        query(
            &db,
            "INSERT INTO bugsnitch_migrations(name,digest) VALUES ($1::text, pg_catalog.encode(pg_catalog.sha256($2::bytea),'hex'))",
            &args,
        )?;
    }
    checked(db.exec("COMMIT"))?;
    Ok(())
}
