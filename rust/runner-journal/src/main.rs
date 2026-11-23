// SPDX-License-Identifier: MPL-2.0
use rusqlite::{
    Batch, Connection, Error, OpenFlags, fallible_iterator::FallibleIterator, types::ValueRef,
};
use serde::Deserialize;
use std::{
    env,
    io::{self, Read, Write},
    path::{Path, PathBuf},
    process::ExitCode,
    time::Duration,
};

mod attempts;
mod controls;
mod integration;
#[cfg(target_os = "macos")]
mod macos_group;
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
    operation: Option<attempts::Operation>,
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

fn open(path: &Path, runner: bool) -> Result<Connection, Failure> {
    let connection = Connection::open_with_flags(
        path,
        OpenFlags::SQLITE_OPEN_READ_WRITE | OpenFlags::SQLITE_OPEN_NO_MUTEX,
    )?;
    connection.busy_timeout(Duration::from_secs(5))?;
    connection.pragma_update(None, "foreign_keys", runner)?;
    Ok(connection)
}

#[cfg(unix)]
#[derive(PartialEq, Eq)]
struct FileIdentity {
    device: u64,
    inode: u64,
}

#[cfg(unix)]
impl FileIdentity {
    fn read(path: &Path) -> Result<Self, Failure> {
        use std::os::unix::fs::MetadataExt;
        let metadata = std::fs::metadata(path).map_err(|_| Failure::Database)?;
        Ok(Self {
            device: metadata.dev(),
            inode: metadata.ino(),
        })
    }
}

#[derive(Default)]
struct RunnerConnection {
    #[cfg(unix)]
    cached: Option<(Connection, FileIdentity)>,
}

impl RunnerConnection {
    #[cfg(unix)]
    fn connection(&mut self, path: &Path) -> Result<&mut Connection, Failure> {
        let before = FileIdentity::read(path)?;
        let reuse = self.cached.as_ref().is_some_and(|(db, identity)| {
            if &before != identity || db.path().map(Path::new) != Some(path) || !db.is_autocommit()
            {
                return false;
            }
            // SQLite checks the inode of its open handle against the path;
            // the filesystem device distinguishes matching inodes across volumes.
            let mut moved = -1;
            db.file_control(
                rusqlite::DEFAULT_NAME,
                rusqlite::FileControl::HasMoved(&mut moved),
            )
            .is_ok()
                && moved == 0
        });
        if !reuse {
            self.cached.take();
            let db = open(path, true)?;
            // If the path changed while SQLite opened it, leave the operation
            // unexecuted. Never associate an uncertain handle with a new file.
            if before != FileIdentity::read(path)? {
                return Err(Failure::Database);
            }
            self.cached = Some((db, before));
        }
        Ok(&mut self.cached.as_mut().ok_or(Failure::Database)?.0)
    }

    fn run(
        &mut self,
        path: &Path,
        op: impl FnOnce(&mut Connection) -> Result<Vec<u8>, Failure>,
    ) -> Result<Vec<u8>, Failure> {
        #[cfg(unix)]
        let db = self.connection(path)?;
        #[cfg(not(unix))]
        let db = &mut open(path, true)?;
        schema::prepare(db, true)?;
        op(db)
    }
}

fn execute(request: Request, cached: &mut RunnerConnection) -> Result<Vec<u8>, Failure> {
    let runner = match request.path.file_name().and_then(|name| name.to_str()) {
        Some("runner.sqlite") => true,
        Some("device.sqlite") => false,
        _ => return Err(Failure::Protocol),
    };
    if request.version != PROTOCOL || !request.path.is_absolute() {
        return Err(Failure::Protocol);
    }
    let sql = match (request.sql, request.control, request.operation) {
        (Some(sql), None, None) => sql,
        (None, Some(control), None) if runner => {
            return cached.run(&request.path, |db| controls::execute(db, control));
        }
        (None, None, Some(attempts::Operation::SyncAuthority { device, workspace })) if !runner => {
            let mut db = open(&request.path, false)?;
            schema::prepare(&mut db, false)?;
            return attempts::sync_authority(&db, &device, &workspace);
        }
        (None, None, Some(op)) if runner => {
            return cached.run(&request.path, |db| attempts::execute(db, op));
        }
        _ => return Err(Failure::Protocol),
    };
    // Compatibility SQL may leave a transaction, attachment, or PRAGMA behind.
    // Its connection is never shared with another batch or a typed operation.
    let mut connection = open(&request.path, runner)?;
    schema::prepare(&mut connection, runner)?;
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
    let mut runner = RunnerConnection::default();
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
            .and_then(|request| execute(request, &mut runner));
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
                "bugsnitch-runner-journal {} protocol={PROTOCOL} sqlite={} capacity=1 execution={} controls=1 attempts=1",
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

    #[cfg(unix)]
    #[test]
    fn cache_reopens_when_inode_matches_on_a_different_device() {
        let path = env::temp_dir().join(format!(
            "bugsnitch-volume-test-{}.sqlite",
            std::process::id()
        ));
        std::fs::File::create_new(&path).unwrap();
        let path = path.canonicalize().unwrap();
        let mut runner = RunnerConnection::default();
        runner
            .connection(&path)
            .unwrap()
            .execute_batch("CREATE TEMP TABLE old_connection(n INTEGER)")
            .unwrap();
        let count = |db: &Connection| {
            db.query_row(
                "SELECT count(*) FROM sqlite_temp_schema WHERE name='old_connection'",
                [],
                |row| row.get::<_, i64>(0),
            )
            .unwrap()
        };
        assert_eq!(count(runner.connection(&path).unwrap()), 1);
        // Model a cached handle from an unmounted volume which allocated
        // the same inode number as this path on the replacement volume.
        runner.cached.as_mut().unwrap().1.device ^= 1;
        assert_eq!(count(runner.connection(&path).unwrap()), 0);
        drop(runner);
        std::fs::remove_file(path).unwrap();
    }

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
