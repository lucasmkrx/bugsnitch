// SPDX-License-Identifier: MPL-2.0
use super::{WebError, WebResult};
use axum::http::StatusCode;
use serde::Deserialize;
use serde_json::Value;
use std::{
    env,
    path::{Path, PathBuf},
    process::Stdio,
    time::Duration,
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::UnixStream,
    process::{Child, Command},
    time::timeout,
};

pub(super) struct Worker {
    pub child: Child,
    pub lifeline: Option<tokio::process::ChildStdin>,
    _directory: tempfile::TempDir,
    pub socket: PathBuf,
}
impl Worker {
    pub async fn start(origin: &str, scheduled: bool) -> Result<Self, &'static str> {
        use std::os::unix::fs::PermissionsExt;
        let directory = tempfile::Builder::new()
            .prefix("bugsnitch-")
            .permissions(std::fs::Permissions::from_mode(0o700))
            .tempdir()
            .map_err(|_| "Cannot create private worker directory")?;
        let socket = directory.path().join("domain.sock");
        let path = env::current_exe()
            .map_err(|_| "Cannot locate server installation")?
            .with_file_name("bugsnitch-domain");
        let mut cmd = Command::new(path);
        cmd.arg(&socket)
            .env_clear()
            .env("BUGSNITCH_ORIGIN", origin)
            .env(
                "BUGSNITCH_BACKGROUND_MODE",
                if scheduled { "scheduled" } else { "continuous" },
            )
            .kill_on_drop(true)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped());
        for (k, v) in env::vars_os() {
            if let Some(key) = k.to_str()
                && (key.starts_with("PG")
                    || matches!(
                        key,
                        "DATABASE_URL"
                            | "HOME"
                            | "USER"
                            | "LOGNAME"
                            | "TMPDIR"
                            | "KRB5CCNAME"
                            | "KRB5_CONFIG"
                            | "SSL_CERT_FILE"
                            | "SSL_CERT_DIR"
                            | "HTTPS_PROXY"
                            | "https_proxy"
                            | "NO_PROXY"
                            | "no_proxy"
                            | "HTTP_PROXY"
                            | "http_proxy"
                            | "ALL_PROXY"
                            | "all_proxy"
                            | "BUGSNITCH_GITHUB_INSTALLATION_ID"
                            | "BUGSNITCH_GITHUB_PROJECT_STATUS_ACTIVE"
                            | "BUGSNITCH_GITHUB_PROJECT_STATUS_DONE"
                            | "BUGSNITCH_GITHUB_QUEUE_REPOSITORIES"
                            | "GITHUB_APP_CLIENT_ID"
                            | "GITHUB_APP_KEY_FILE"
                            | "BUGSNITCH_MIGRATOR"
                            | "BUGSNITCH_PROVIDER"
                            | "BUGSNITCH_LINEAR_AGENT_TOKEN"
                            | "BUGSNITCH_LINEAR_API_KEY"
                    ))
            {
                cmd.env(k, v);
            }
        }
        let mut child = cmd
            .spawn()
            .map_err(|_| "Install bugsnitch-domain beside the Rust server")?;
        let mut pipe = child
            .stdout
            .take()
            .ok_or("Domain startup pipe unavailable")?;
        if !matches!(
            timeout(Duration::from_secs(35), pipe.read_u8()).await,
            Ok(Ok(1))
        ) {
            return Err(
                "Domain startup failed. Check the matching executables, database and GitHub App configuration",
            );
        }
        let lifeline = child.stdin.take();
        Ok(Self {
            child,
            lifeline,
            socket,
            _directory: directory,
        })
    }
}

#[derive(serde::Serialize)]
struct CommandFrame<'a> {
    version: u8,
    operation: &'a str,
    actor: [String; 2],
    input: Value,
}
#[derive(Deserialize)]
struct Reply {
    version: u8,
    status: u16,
    value: Option<Value>,
    error: Option<String>,
}
pub(super) async fn call(
    socket: &Path,
    op: &str,
    actor: [String; 2],
    input: Value,
) -> WebResult<Value> {
    let mut stream = UnixStream::connect(socket).await.map_err(|_| {
        WebError(
            StatusCode::SERVICE_UNAVAILABLE,
            "Domain worker unavailable".into(),
        )
    })?;
    let payload = serde_json::to_vec(&CommandFrame {
        version: 1,
        operation: op,
        actor,
        input,
    })
    .map_err(|_| WebError::internal())?;
    if payload.len() > 256 << 10 {
        return Err(WebError(
            StatusCode::PAYLOAD_TOO_LARGE,
            "Command too large".into(),
        ));
    }
    let uncertain = || {
        WebError(
            StatusCode::SERVICE_UNAVAILABLE,
            "Operation outcome unknown. Refresh its status before retrying.".into(),
        )
    };
    stream
        .write_u32(payload.len() as u32)
        .await
        .map_err(|_| uncertain())?;
    stream.write_all(&payload).await.map_err(|_| uncertain())?;
    let n = stream.read_u32().await.map_err(|_| uncertain())? as usize;
    if n == 0 || n > 32 << 20 {
        return Err(uncertain());
    }
    let mut buf = vec![0; n];
    stream.read_exact(&mut buf).await.map_err(|_| uncertain())?;
    let reply: Reply = serde_json::from_slice(&buf).map_err(|_| uncertain())?;
    if reply.version != 1 {
        return Err(uncertain());
    }
    match (reply.status, reply.value, reply.error) {
        (200, Some(value), None) => Ok(value),
        (status, None, Some(error)) if (400..600).contains(&status) => Err(WebError(
            StatusCode::from_u16(status).map_err(|_| uncertain())?,
            super::ErrorBody { error, code: None },
        )),
        _ => Err(uncertain()),
    }
}
