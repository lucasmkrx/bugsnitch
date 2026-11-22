// SPDX-License-Identifier: MPL-2.0
use crate::{claims, config, devices, events, jobs};
use libpq::{Connection, Format, PQResult, Status};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    cell::Cell,
    env,
    ffi::CString,
    io::{self, Read, Write},
};

pub type Result<T> = std::result::Result<T, Error>;
pub(super) const MAX_FRAME: usize = 4 << 20;

pub(super) fn response_limit(op: &str) -> usize {
    // Public workspace reads and MCP projections previously used the domain
    // socket's 32 MiB reply budget. Mutation inputs stay bounded at 4 MiB.
    if matches!(op, "workspace.state" | "workspace.diagnostics") || crate::plugin_reads::is_list(op)
    {
        32 << 20
    } else {
        MAX_FRAME
    }
}

enum ReplyValue {
    Structured(Value),
    // PostgreSQL produces the projection as a single JSON document. Validate
    // it once without building another tree of every record and key.
    Projection(Box<serde_json::value::RawValue>),
}

// PostgreSQL's JSON aggregates include separator whitespace. Drop only JSON
// whitespace outside strings, in the original allocation, to keep public
// payloads compact without deserializing and serializing every object.
fn compact_projection(document: String) -> Result<Box<serde_json::value::RawValue>> {
    let mut bytes = document.into_bytes();
    let mut quoted = false;
    let mut escaped = false;
    bytes.retain(|&byte| {
        if escaped {
            escaped = false;
        } else if byte == b'"' {
            quoted = !quoted;
        } else if quoted && byte == b'\\' {
            escaped = true;
        } else if !quoted && matches!(byte, b' ' | b'\t' | b'\r' | b'\n') {
            return false;
        }
        true
    });
    let document = String::from_utf8(bytes).map_err(|_| Error::internal())?;
    serde_json::value::RawValue::from_string(document).map_err(|_| Error::internal())
}

#[derive(Debug, Serialize)]
pub struct Error {
    pub(crate) status: u16,
    pub(crate) message: String,
}

impl Error {
    fn new(status: u16, message: &str) -> Self {
        Self {
            status,
            message: message.into(),
        }
    }
    pub fn bad(message: &str) -> Self {
        Self::new(400, message)
    }
    pub fn unauthenticated() -> Self {
        Self::new(401, "Session expired")
    }
    pub fn denied() -> Self {
        Self::new(403, "Permission denied")
    }
    pub fn missing() -> Self {
        Self::new(404, "Record unavailable")
    }
    pub fn conflict(message: &str) -> Self {
        Self::new(409, message)
    }
    pub fn internal() -> Self {
        Self::new(500, "Coordination operation failed")
    }
}
impl From<serde_json::Error> for Error {
    fn from(_: serde_json::Error) -> Self {
        Self::bad("Invalid coordination payload")
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    version: u8,
    op: String,
    actor: [String; 2],
    input: Value,
}

#[derive(Default)]
pub struct Details<'a> {
    pub reason: &'a str,
    pub policy: i64,
    pub version: i64,
    pub evidence: &'a str,
}

// Keep only the two large, fixed dashboard statements on a worker's own
// PostgreSQL session. Ordinary coordinator calls never prepare them.
struct ProjectionPlans {
    prefix: String,
    prepared: [Cell<bool>; 2],
    disabled: Cell<bool>,
}

pub struct Db<'a> {
    conn: &'a Connection,
    actor: (&'a str, &'a str),
    plans: &'a ProjectionPlans,
}
pub struct Row(Vec<Option<String>>);
impl Row {
    pub fn into_text(mut self) -> Result<String> {
        if self.0.len() != 1 {
            return Err(Error::internal());
        }
        self.0.pop().flatten().ok_or_else(Error::internal)
    }
    pub fn opt(&self, i: usize) -> Result<Option<&str>> {
        self.0
            .get(i)
            .map(|s| s.as_deref())
            .ok_or_else(Error::internal)
    }
    pub fn text(&self, i: usize) -> Result<&str> {
        self.opt(i)?.ok_or_else(Error::internal)
    }
    pub fn i64(&self, i: usize) -> Result<i64> {
        self.text(i)?.parse().map_err(|_| Error::internal())
    }
    pub fn bool(&self, i: usize) -> Result<bool> {
        match self.text(i)? {
            "t" | "true" => Ok(true),
            "f" | "false" => Ok(false),
            _ => Err(Error::internal()),
        }
    }
}

fn checked(r: PQResult) -> Result<PQResult> {
    match r.status() {
        Status::TuplesOk | Status::CommandOk => Ok(r),
        _ => match r
            .error_field(libpq::result::ErrorField::Sqlstate)
            .ok()
            .flatten()
        {
            Some("23505") => Err(Error::conflict("Record already exists")),
            Some("23503") => Err(Error::new(404, "Related record unavailable")),
            _ => Err(Error::internal()),
        },
    }
}

impl Db<'_> {
    pub fn actor(&self) -> (&str, &str) {
        self.actor
    }
    fn query(&self, sql: &str, params: &[&str]) -> Result<PQResult> {
        // libpq text parameters are NUL-terminated. Reject embedded NUL instead of truncating.
        let owned = params
            .iter()
            .map(|p| CString::new(*p).map_err(|_| Error::bad("Invalid text field")))
            .collect::<Result<Vec<_>>>()?;
        let values = owned
            .iter()
            .map(|p| Some(p.as_bytes_with_nul()))
            .collect::<Vec<_>>();
        checked(self.conn.exec_params(
            sql,
            &[],
            &values,
            &vec![Format::Text; values.len()],
            Format::Text,
        ))
    }
    pub fn projection(&self, op: &str, sql: &str, session_hash: &str) -> Result<PQResult> {
        let slot = match op {
            "workspace.state" => 0,
            "workspace.diagnostics" => 1,
            _ => return Err(Error::internal()),
        };
        let uncached = || self.query(sql, &[session_hash]);
        if self.plans.disabled.get() {
            return uncached();
        }
        let name = format!("{}_{}", self.plans.prefix, slot);
        let prepared = &self.plans.prepared[slot];
        if !prepared.get() {
            checked(self.conn.prepare(Some(&name), sql, &[]))?;
            prepared.set(true);
        }
        let hash = CString::new(session_hash).map_err(|_| Error::bad("Invalid session hash"))?;
        let raw = self.conn.exec_prepared(
            Some(&name),
            &[Some(hash.as_bytes_with_nul())],
            &[Format::Text],
            Format::Text,
        );
        if raw
            .error_field(libpq::result::ErrorField::Sqlstate)
            .ok()
            .flatten()
            == Some("26000")
        {
            // A transaction-pooling proxy dropped the prepared statement. A
            // read can safely fall back inside the original snapshot.
            checked(self.conn.exec("ROLLBACK TO SAVEPOINT projection_plan"))?;
            self.plans.disabled.set(true);
            return uncached();
        }
        checked(raw)
    }
    pub fn rows(&self, sql: &str, params: &[&str]) -> Result<Vec<Row>> {
        let r = self.query(sql, params)?;
        (0..r.ntuples())
            .map(|i| {
                let fields = (0..r.nfields())
                    .map(|j| {
                        r.value(i, j)
                            .map(|b| {
                                str::from_utf8(b)
                                    .map(str::to_owned)
                                    .map_err(|_| Error::internal())
                            })
                            .transpose()
                    })
                    .collect::<Result<Vec<_>>>()?;
                Ok(Row(fields))
            })
            .collect()
    }
    pub fn optional(&self, sql: &str, params: &[&str]) -> Result<Option<Row>> {
        let mut rows = self.rows(sql, params)?;
        if rows.len() > 1 {
            return Err(Error::internal());
        }
        Ok(rows.pop())
    }
    pub fn one(&self, sql: &str, params: &[&str]) -> Result<Row> {
        self.optional(sql, params)?.ok_or_else(Error::missing)
    }
    pub fn exec(&self, sql: &str, params: &[&str]) -> Result<u64> {
        self.query(sql, params)?
            .cmd_tuples()
            .map(|n| n as u64)
            .map_err(|_| Error::internal())
    }
    pub fn event(
        &self,
        ws: &str,
        project: &str,
        kind: &str,
        subject: &str,
        actor: (&str, &str),
        details: Details<'_>,
    ) -> Result<()> {
        self.exec(
            include_str!("../../../internal/audit/record.sql"),
            &[
                ws,
                project,
                kind,
                subject,
                actor.0,
                actor.1,
                details.reason,
                &details.policy.to_string(),
                &details.version.to_string(),
                details.evidence,
            ],
        )?;
        Ok(())
    }
}

fn transaction(
    conn: &Connection,
    req: Request,
    schema: Option<&str>,
    plans: &ProjectionPlans,
) -> Result<ReplyValue> {
    if req.version != 1 {
        return Err(Error::bad("Unsupported coordination protocol"));
    }
    let db = Db {
        conn,
        plans,
        actor: (&req.actor[0], &req.actor[1]),
    };
    // Keep settings transaction-local even when an operator uses pooled
    // PostgreSQL connections. Send static setup statements in one round trip.
    let begin = match req.op.as_str() {
        "claims.next" | "workspace.state" | "workspace.diagnostics" => {
            "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;"
        }
        op if crate::plugin_reads::is_list(op) => "BEGIN READ ONLY;",
        _ => "BEGIN;",
    };
    let cached_read = matches!(req.op.as_str(), "workspace.state" | "workspace.diagnostics")
        && !plans.disabled.get();
    let cache = if cached_read {
        if schema.is_some() {
            " SET LOCAL plan_cache_mode=force_generic_plan;"
        } else {
            " SET LOCAL plan_cache_mode=force_generic_plan; SAVEPOINT projection_plan;"
        }
    } else {
        ""
    };
    checked(conn.exec(&format!("{begin} SET LOCAL statement_timeout='12s'; SET LOCAL client_min_messages=ERROR; SET LOCAL TIME ZONE 'UTC';{cache}")))?;
    // One request owns one session until COMMIT or ROLLBACK. A closed parent
    // pipe or killed worker cannot cause the host to repeat the operation.
    let result = (|| {
        if let Some(schema) = schema {
            db.one(
                "SELECT pg_catalog.set_config('search_path',$1,true)",
                &[schema],
            )?;
            if cached_read {
                checked(conn.exec("SAVEPOINT projection_plan"))?;
            }
        }
        if matches!(req.op.as_str(), "workspace.state" | "workspace.diagnostics")
            || crate::plugin_reads::is_list(&req.op)
        {
            let document = if crate::plugin_reads::is_list(&req.op) {
                crate::plugin_reads::list(&db, &req.op, req.input)?
            } else {
                crate::snapshot::read(&db, &req.op, req.input)?
            };
            // List JSON is embedded as MCP text, so separator whitespace
            // has a wire cost. Large dashboard values can normally pass through
            // unchanged. Compact at the ceiling to retain compatibility with
            // previously accepted documents.
            let raw = if crate::plugin_reads::is_list(&req.op)
                || document.len() >= response_limit(&req.op) - 32
            {
                compact_projection(document)?
            } else {
                serde_json::value::RawValue::from_string(document).map_err(|_| Error::internal())?
            };
            return Ok(ReplyValue::Projection(raw));
        }
        match req.op.split('.').next() {
            Some("session") => crate::http::session::apply(&db, &req.op, req.input),
            Some("claims") => claims::handle(&db, &req.op, req.input),
            Some("device") => devices::handle(&db, &req.op, req.input),
            Some("hooks") => crate::http::integrations::hooks::apply(&db, &req.op, req.input),
            Some("jobs") => jobs::handle(&db, &req.op, req.input),
            Some("events") => events::handle(&db, &req.op, req.input),
            _ if matches!(req.op.as_str(), "system.ready" | "system.http_ready") => {
                db.one("SELECT 1", &[])?;
                Ok(json!(1))
            }
            _ => Err(Error::bad("Unknown coordination operation")),
        }
        .map(ReplyValue::Structured)
    })();
    match result {
        Ok(value) => {
            let commit = conn.exec("COMMIT");
            if commit.status() != Status::CommandOk
                && commit
                    .error_field(libpq::result::ErrorField::Sqlstate)
                    .ok()
                    .flatten()
                    .is_none_or(|code| code.starts_with("08") || code == "57P01" || code == "57P02")
            {
                return Err(Error::new(
                    503,
                    "Operation outcome unknown. Refresh its status before retrying.",
                ));
            }
            let commit = checked(commit)?;
            if commit.cmd_status().ok().flatten().as_deref() != Some("COMMIT") {
                return Err(Error::internal());
            }
            Ok(value)
        }
        Err(e) => {
            checked(conn.exec("ROLLBACK"))?;
            Err(e)
        }
    }
}

// The HTTP parent normally kills a stuck request after 15 seconds. This second
// deadline also covers abrupt parent death during a blocking libpq syscall.
fn watchdog() -> std::sync::mpsc::Sender<bool> {
    use std::{sync::mpsc, thread, time::Duration};
    let (tx, rx) = mpsc::channel();
    thread::spawn(move || {
        let mut active = true; // initial database connection
        loop {
            if active {
                match rx.recv_timeout(Duration::from_secs(20)) {
                    Ok(next) => active = next,
                    Err(mpsc::RecvTimeoutError::Disconnected) => return,
                    Err(mpsc::RecvTimeoutError::Timeout) => std::process::exit(1),
                }
            } else {
                match rx.recv() {
                    Ok(next) => active = next,
                    Err(_) => return,
                }
            }
        }
    });
    tx
}

// For a large projection PostgreSQL has already produced a bounded JSON
// document. Write the frame in parts so its envelope needs no second buffer.
fn write_reply(
    output: &mut impl Write,
    result: Result<ReplyValue>,
    limit: usize,
) -> io::Result<()> {
    let encoded;
    let (prefix, payload, suffix): (&[u8], &[u8], &[u8]) = match &result {
        Ok(ReplyValue::Projection(raw)) => {
            (br#"{"version":1,"value":"#, raw.get().as_bytes(), b"}")
        }
        Ok(ReplyValue::Structured(value)) => {
            encoded = serde_json::to_vec(value)?;
            (br#"{"version":1,"value":"#, &encoded, b"}")
        }
        Err(error) => {
            encoded = serde_json::to_vec(error)?;
            (br#"{"version":1,"error":"#, &encoded, b"}")
        }
    };
    let len = prefix.len() + payload.len() + suffix.len();
    if len > limit {
        return Err(io::Error::other("Coordination response too large"));
    }
    output.write_all(&(len as u32).to_be_bytes())?;
    output.write_all(prefix)?;
    output.write_all(payload)?;
    output.write_all(suffix)?;
    output.flush()
}

pub fn serve() -> io::Result<()> {
    let invalid = || io::Error::other("coordination service unavailable");
    let settings =
        config::split(&env::var("DATABASE_URL").map_err(|_| invalid())?).map_err(|_| invalid())?;
    let mut nonce = [0u8; 12];
    getrandom::fill(&mut nonce).map_err(|_| invalid())?;
    let plans = ProjectionPlans {
        prefix: format!("bugsnitch_{}", hex::encode(nonce)),
        prepared: Default::default(),
        disabled: Cell::new(false),
    };
    let guard = watchdog();
    let conn = Connection::new(&settings.native).map_err(|_| invalid())?;
    guard.send(false).map_err(|_| invalid())?;
    let mut input = io::stdin().lock();
    let mut output = io::stdout().lock();
    loop {
        let mut header = [0; 4];
        if input.read(&mut header[..1])? == 0 {
            return Ok(());
        }
        guard.send(true).map_err(|_| invalid())?;
        input.read_exact(&mut header[1..])?;
        let n = u32::from_be_bytes(header) as usize;
        if n > MAX_FRAME {
            return Err(invalid());
        }
        let mut data = vec![0; n];
        input.read_exact(&mut data)?;
        let request = serde_json::from_slice::<Request>(&data);
        let limit = request
            .as_ref()
            .map_or(MAX_FRAME, |r| response_limit(&r.op));
        let result = request
            .map_err(Error::from)
            .and_then(|req| transaction(&conn, req, settings.search_path.as_deref(), &plans));
        write_reply(&mut output, result, limit)?;
        guard.send(false).map_err(|_| invalid())?;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn projection_frame_is_bounded_before_writing_its_header() {
        let document =
            || ReplyValue::Projection(compact_projection(r#"[ "record" ]"#.into()).unwrap());
        let mut bytes = Vec::new();
        write_reply(&mut bytes, Ok(document()), 100).unwrap();
        assert_eq!(
            u32::from_be_bytes(bytes[..4].try_into().unwrap()) as usize,
            bytes.len() - 4
        );
        assert_eq!(&bytes[4..], br#"{"version":1,"value":["record"]}"#);
        let mut incomplete = Vec::new();
        assert!(write_reply(&mut incomplete, Ok(document()), bytes.len() - 5).is_err());
        assert!(incomplete.is_empty());
    }

    #[test]
    fn projection_compaction_preserves_escaped_content_and_numbers() {
        let input = r#"
          { "data": ["🦀  spaces", " tabs\t ", "\\", "escaped \"  quote", 1.230e+1],
            "empty": [  ], "value with spaces ": null }
        "#;
        let raw = compact_projection(input.into()).unwrap();
        assert_eq!(
            serde_json::from_str::<Value>(input).unwrap(),
            serde_json::from_str::<Value>(raw.get()).unwrap()
        );
        assert!(raw.get().contains("1.230e+1"));
        assert!(raw.get().contains(r#""empty":[]"#));
    }
}
