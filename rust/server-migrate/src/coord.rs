// SPDX-License-Identifier: MPL-2.0
use crate::{claims, config, devices, events, jobs};
use libpq::{Connection, Format, PQResult, Status};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    env,
    ffi::CString,
    io::{self, Read, Write},
};

pub type Result<T> = std::result::Result<T, Error>;
const MAX_FRAME: usize = 4 << 20;

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

pub struct Db<'a> {
    conn: &'a Connection,
    actor: (&'a str, &'a str),
}
pub struct Row(Vec<Option<String>>);
impl Row {
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

fn transaction(conn: &Connection, req: Request, schema: Option<&str>) -> Result<Value> {
    if req.version != 1 {
        return Err(Error::bad("Unsupported coordination protocol"));
    }
    let db = Db {
        conn,
        actor: (&req.actor[0], &req.actor[1]),
    };
    let begin = if req.op == "claims.next" {
        "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"
    } else {
        "BEGIN"
    };
    checked(conn.exec(begin))?;
    // One request owns one session until COMMIT or ROLLBACK. A closed parent
    // pipe or killed worker cannot cause the host to repeat the operation.
    let result = (|| {
        if let Some(schema) = schema {
            db.one(
                "SELECT pg_catalog.set_config('search_path',$1,true)",
                &[schema],
            )?;
        }
        checked(conn.exec("SET LOCAL statement_timeout='12s'; SET LOCAL client_min_messages=ERROR; SET LOCAL TIME ZONE 'UTC'"))?;
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

pub fn serve() -> io::Result<()> {
    let invalid = || io::Error::other("coordination service unavailable");
    let settings =
        config::split(&env::var("DATABASE_URL").map_err(|_| invalid())?).map_err(|_| invalid())?;
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
        let result = serde_json::from_slice(&data)
            .map_err(Error::from)
            .and_then(|req| transaction(&conn, req, settings.search_path.as_deref()));
        let response = match result {
            Ok(value) => json!({"version":1,"value":value}),
            Err(e) => json!({"version":1,"error":e}),
        };
        let frame = serde_json::to_vec(&response)?;
        if frame.len() > MAX_FRAME {
            return Err(invalid());
        }
        output.write_all(&(frame.len() as u32).to_be_bytes())?;
        output.write_all(&frame)?;
        output.flush()?;
        guard.send(false).map_err(|_| invalid())?;
    }
}
