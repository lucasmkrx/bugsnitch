// SPDX-License-Identifier: MPL-2.0
use rusqlite::{
    Batch, Connection, Error, OpenFlags, fallible_iterator::FallibleIterator, types::ValueRef,
};
use serde::Deserialize;
use std::{
    env,
    io::{self, Read, Write},
    path::PathBuf,
    process::ExitCode,
    time::Duration,
};

mod controls;
mod integration;
mod operations;
mod schema;
mod validation;
mod worktree;

const PROTOCOL: u8 = 1;
const MAX_FRAME: usize = 64 * 1024 * 1024;

// Local pipes only. Each request has a big-endian u32 length followed by JSON.
// Each reply has a status byte, a big-endian u32 length and raw list-mode bytes.
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    version: u8,
    path: PathBuf,
    sql: Option<String>,
    control: Option<controls::Control>,
}

#[repr(u8)]
#[derive(Clone, Copy, Debug)]
enum Failure {
    Protocol = 1,
    Database = 2,
    Busy = 3,
    Constraint = 4,
    TooLarge = 5,
    Schema = 6,
}

impl From<Error> for Failure {
    fn from(error: Error) -> Self {
        match error.sqlite_error_code() {
            Some(rusqlite::ErrorCode::DatabaseBusy | rusqlite::ErrorCode::DatabaseLocked) => {
                Self::Busy
            }
            Some(rusqlite::ErrorCode::ConstraintViolation) => Self::Constraint,
            _ => Self::Database,
        }
    }
}

fn append(output: &mut Vec<u8>, bytes: &[u8]) -> Result<(), Failure> {
    if bytes.len() > MAX_FRAME - output.len() {
        return Err(Failure::TooLarge);
    }
    output.extend_from_slice(bytes);
    Ok(())
}

fn execute(request: Request) -> Result<Vec<u8>, Failure> {
    let runner = match request.path.file_name().and_then(|name| name.to_str()) {
        Some("runner.sqlite") => true,
        Some("device.sqlite") => false,
        _ => return Err(Failure::Protocol),
    };
    if request.version != PROTOCOL || !request.path.is_absolute() {
        return Err(Failure::Protocol);
    }
    // The parent creates the owner's file privately. A new connection preserves
    // CLI invocation isolation: an unfinished transaction rolls back on drop,
    // per-connection PRAGMAs reset, and a restored/replaced file is reopened.
    let mut connection = Connection::open_with_flags(
        request.path,
        OpenFlags::SQLITE_OPEN_READ_WRITE | OpenFlags::SQLITE_OPEN_NO_MUTEX,
    )?;
    connection.busy_timeout(Duration::from_secs(5))?;
    connection.pragma_update(None, "foreign_keys", runner)?;
    schema::prepare(&mut connection, runner)?;
    let sql = match (request.sql, request.control) {
        (Some(sql), None) => sql,
        (None, Some(control)) if runner => return controls::execute(&mut connection, control),
        _ => return Err(Failure::Protocol),
    };
    let mut batch = Batch::new(&connection, &sql);
    let mut output = Vec::new();
    while let Some(mut statement) = batch.next()? {
        let columns = statement.column_count();
        let mut rows = statement.query([])?;
        while let Some(row) = rows.next()? {
            for col in 0..columns {
                if col > 0 {
                    append(&mut output, b"|")?;
                }
                match row.get_ref(col)? {
                    ValueRef::Null => {}
                    ValueRef::Integer(value) => append(&mut output, value.to_string().as_bytes())?,
                    ValueRef::Real(value) => {
                        let value: String =
                            connection
                                .query_row("SELECT CAST(?1 AS TEXT)", [value], |row| row.get(0))?;
                        append(&mut output, value.as_bytes())?;
                    }
                    ValueRef::Text(value) | ValueRef::Blob(value) => append(&mut output, value)?,
                }
            }
            append(&mut output, b"\n")?;
        }
    }
    Ok(output)
}

fn serve(mut input: impl Read, mut output: impl Write) -> io::Result<()> {
    loop {
        let mut length = [0; 4];
        if input.read(&mut length[..1])? == 0 {
            return Ok(());
        }
        input.read_exact(&mut length[1..])?;
        let size = u32::from_be_bytes(length) as usize;
        if size > MAX_FRAME {
            return Err(io::Error::from(io::ErrorKind::InvalidData));
        }
        let mut frame = vec![0; size];
        input.read_exact(&mut frame)?;
        let result = serde_json::from_slice(&frame)
            .map_err(|_| Failure::Protocol)
            .and_then(execute);
        let (status, bytes) = match result {
            Ok(bytes) => (0, bytes),
            Err(failure) => (failure as u8, Vec::new()),
        };
        output.write_all(&[status])?;
        output.write_all(&(bytes.len() as u32).to_be_bytes())?;
        output.write_all(&bytes)?;
        output.flush()?;
    }
}

fn main() -> ExitCode {
    let args: Vec<_> = env::args_os().skip(1).collect();
    match args.as_slice() {
        [arg] if arg == "--version" => {
            println!(
                "bugsnitch-runner-journal {} protocol={PROTOCOL} sqlite={} execution={} controls=1",
                env!("CARGO_PKG_VERSION"),
                rusqlite::version(),
                u8::from(cfg!(any(target_os = "macos", target_os = "linux"))),
            );
            ExitCode::SUCCESS
        }
        [arg] if arg == "--validate" => validation::run(),
        [arg] if arg == "--worktree" || arg == "--integration" => {
            let op = if arg == "--worktree" {
                worktree::run
            } else {
                integration::run
            };
            match operations::serve(op) {
                Ok(()) => ExitCode::SUCCESS,
                Err(_) => ExitCode::FAILURE,
            }
        }
        [arg] if arg == "--stdio" => match serve(io::stdin().lock(), io::stdout().lock()) {
            Ok(()) => ExitCode::SUCCESS,
            Err(_) => ExitCode::FAILURE,
        },
        _ => {
            eprintln!("use --stdio, --worktree, --integration, --validate or --version");
            ExitCode::FAILURE
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn invalid_requests_can_be_followed_by_another_frame() {
        let mut input = Vec::new();
        for invalid in [
            b"{}" as &[u8],
            br#"{"version":2,"path":"runner.sqlite","sql":"select 1"}"#,
        ] {
            input.extend_from_slice(&(invalid.len() as u32).to_be_bytes());
            input.extend_from_slice(invalid);
        }
        let mut output = Vec::new();
        serve(input.as_slice(), &mut output).unwrap();
        assert_eq!(output, [1, 0, 0, 0, 0, 1, 0, 0, 0, 0]);
    }

    #[test]
    fn truncated_or_excessive_frames_stop_the_stream() {
        for invalid in [
            vec![0, 0, 0],
            vec![0, 0, 0, 3, b'{'],
            ((MAX_FRAME + 1) as u32).to_be_bytes().to_vec(),
        ] {
            let mut output = Vec::new();
            assert!(serve(invalid.as_slice(), &mut output).is_err());
            assert!(output.is_empty());
        }
    }
}
