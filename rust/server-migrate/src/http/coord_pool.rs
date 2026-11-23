// SPDX-License-Identifier: MPL-2.0
use super::{ErrorBody, WebError, WebResult};
use crate::coord::{MAX_FRAME, response_limit};
use axum::http::StatusCode;
use bytes::Bytes;
use serde::Deserialize;
use serde_json::Value;
use std::{
    env, io,
    path::PathBuf,
    process::Stdio,
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    process::{Child, ChildStdin, ChildStdout, Command},
    sync::{Semaphore, oneshot},
    time::timeout,
};

#[derive(serde::Serialize)]
struct CommandFrame<'a> {
    version: u8,
    #[serde(rename = "op")]
    operation: &'a str,
    actor: [String; 2],
    input: Value,
}
#[derive(Deserialize)]
struct Failure {
    status: u16,
    message: String,
    code: Option<String>,
}
struct Session {
    child: Child,
    reader: ChildStdout,
    writer: ChildStdin,
}
pub(super) struct Pool {
    path: PathBuf,
    idle: Mutex<Vec<Session>>,
    permits: Arc<Semaphore>,
}
fn uncertain() -> WebError {
    WebError(
        StatusCode::SERVICE_UNAVAILABLE,
        "Operation outcome unknown. Refresh its status before retrying.".into(),
    )
}

impl Pool {
    pub fn new() -> Result<Arc<Self>, &'static str> {
        let path = match env::var_os("BUGSNITCH_MIGRATOR") {
            Some(p) => PathBuf::from(p),
            None => env::current_exe()
                .map_err(|_| "Cannot locate coordinator")?
                .with_file_name("bugsnitch-server-migrate"),
        };
        if !path.is_absolute() {
            return Err("BUGSNITCH_MIGRATOR must be an absolute path");
        }
        Ok(Arc::new(Self {
            path,
            idle: Default::default(),
            permits: Arc::new(Semaphore::new(4)),
        }))
    }
    fn start(&self) -> WebResult<Session> {
        let mut cmd = Command::new(&self.path);
        cmd.arg("--coordinate")
            .env_clear()
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .kill_on_drop(true);
        for (k, v) in env::vars_os() {
            if let Some(key) = k.to_str()
                && (key.starts_with("PG")
                    || matches!(
                        key,
                        "DATABASE_URL"
                            | "HOME"
                            | "USER"
                            | "LOGNAME"
                            | "KRB5CCNAME"
                            | "KRB5_CONFIG"
                            | "SSL_CERT_FILE"
                            | "SSL_CERT_DIR"
                    ))
            {
                cmd.env(k, v);
            }
        }
        let mut child = cmd.spawn().map_err(|_| {
            WebError(
                StatusCode::SERVICE_UNAVAILABLE,
                "Install the matching Rust coordinator".into(),
            )
        })?;
        let reader = child.stdout.take().ok_or_else(WebError::internal)?;
        let writer = child.stdin.take().ok_or_else(WebError::internal)?;
        Ok(Session {
            child,
            reader,
            writer,
        })
    }
    pub async fn call(
        self: &Arc<Self>,
        operation: &str,
        actor: [String; 2],
        input: Value,
    ) -> WebResult<Value> {
        self.request(operation, actor, input, |bytes| decode::<Value>(&bytes))
            .await
    }

    pub async fn raw(
        self: &Arc<Self>,
        operation: &str,
        actor: [String; 2],
        input: Value,
    ) -> WebResult<Bytes> {
        self.request(operation, actor, input, |bytes| {
            let value: &serde_json::value::RawValue = decode(&bytes)?;
            Ok(bytes.slice_ref(value.get().as_bytes()))
        })
        .await
    }

    async fn request<T: Send + 'static>(
        self: &Arc<Self>,
        operation: &str,
        actor: [String; 2],
        input: Value,
        decode: fn(Bytes) -> WebResult<T>,
    ) -> WebResult<T> {
        let limit = response_limit(operation);
        let bytes = serde_json::to_vec(&CommandFrame {
            version: 1,
            operation,
            actor,
            input,
        })
        .map_err(|_| WebError::internal())?;
        if bytes.len() > MAX_FRAME {
            return Err(WebError(
                StatusCode::PAYLOAD_TOO_LARGE,
                "Operation too large".into(),
            ));
        }
        let slot = self
            .permits
            .clone()
            .acquire_owned()
            .await
            .map_err(|_| WebError::internal())?;
        let pool = self.clone();
        let (mut reply, receive) = oneshot::channel();
        tokio::spawn(async move {
            let _slot = slot;
            let idle = pool.idle.lock().expect("coordinator pool").pop();
            let mut session = match idle.map(Ok).unwrap_or_else(|| pool.start()) {
                Ok(s) => s,
                Err(e) => {
                    let _ = reply.send(Err(e));
                    return;
                }
            };
            let outcome = tokio::select! {
                _=reply.closed()=>Err(uncertain()),
                result=timeout(Duration::from_secs(15),session.exchange(&bytes, limit))=>result.unwrap_or_else(|_|Err(uncertain())).and_then(decode),
            };
            if !matches!(&outcome,Err(e) if e.0.is_server_error()) {
                pool.idle.lock().expect("coordinator pool").push(session);
            } else {
                // Reap before releasing the permit, including loss of the HTTP client.
                let _ = session.child.kill().await;
                let _ = session.child.wait().await;
            }
            let _ = reply.send(outcome);
        });
        receive.await.map_err(|_| uncertain())?
    }
}
impl Session {
    async fn exchange(&mut self, b: &[u8], limit: usize) -> WebResult<Bytes> {
        let read = async {
            self.writer.write_u32(b.len() as u32).await?;
            self.writer.write_all(b).await?;
            self.writer.flush().await?;
            let n = self.reader.read_u32().await? as usize;
            if n == 0 || n > limit {
                return Err(io::Error::other("Invalid frame"));
            }
            let mut bytes = vec![0; n];
            self.reader.read_exact(&mut bytes).await?;
            Ok(bytes)
        }
        .await
        .map_err(|_| uncertain())?;
        Ok(read.into())
    }
}

// A successful operation may return an explicit JSON null. Distinguish it
// from an absent value and reject ambiguous or malformed helper responses.
fn present<'de, D: serde::Deserializer<'de>, T: Deserialize<'de>>(
    d: D,
) -> Result<Option<T>, D::Error> {
    T::deserialize(d).map(Some)
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, bound(deserialize = "T: Deserialize<'de>"))]
struct Reply<T> {
    version: u8,
    #[serde(default, deserialize_with = "present")]
    value: Option<T>,
    #[serde(default, deserialize_with = "present")]
    error: Option<Failure>,
}

fn decode<'de, T: Deserialize<'de>>(bytes: &'de [u8]) -> WebResult<T> {
    let reply: Reply<T> = serde_json::from_slice(bytes).map_err(|_| uncertain())?;
    match (reply.version, reply.value, reply.error) {
        (1, Some(v), None) => Ok(v),
        (1, None, Some(e)) => {
            let status = StatusCode::from_u16(e.status).map_err(|_| uncertain())?;
            if status.is_client_error() || status.is_server_error() {
                Err(WebError(
                    status,
                    ErrorBody {
                        error: e.message,
                        code: e.code,
                    },
                ))
            } else {
                Err(uncertain())
            }
        }
        _ => Err(uncertain()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{fs, os::unix::fs::PermissionsExt};

    #[test]
    fn helper_frames_preserve_raw_values_and_reject_ambiguous_replies() {
        use serde_json::value::RawValue;
        let bytes =
            Bytes::from_static(br#"{ "value": {"escaped":"a\"b", "key":null}, "version":1 }"#);
        let borrowed = decode::<&RawValue>(&bytes).unwrap().get().as_bytes();
        assert_eq!(
            bytes.slice_ref(borrowed),
            &br#"{"escaped":"a\"b", "key":null}"#[..]
        );
        assert_eq!(
            decode::<Value>(br#"{"version":1,"value":null}"#).unwrap(),
            Value::Null
        );
        for invalid in [
            r#"{"version":2,"value":true}"#,
            r#"{"version":1,"value":1,"value":2}"#,
            r#"{"version":1,"value":3,"error":null}"#,
            r#"{"version":1,"value":3,"error":{"status":403,"message":"x"}}"#,
            r#"{"version":1,"error":{"status":200,"message":"x"}}"#,
            r#"{"version":1,"value":[}"#,
        ] {
            assert_eq!(
                decode::<&RawValue>(invalid.as_bytes()).unwrap_err().0,
                StatusCode::SERVICE_UNAVAILABLE
            );
        }
        let denied = decode::<Value>(
            br#"{"version":1,"error":{"status":403,"message":"Permission denied"}}"#,
        )
        .unwrap_err();
        assert_eq!(denied.0, StatusCode::FORBIDDEN);
    }

    #[tokio::test]
    async fn disconnected_callers_release_all_native_slots() {
        let temp = tempfile::tempdir().unwrap();
        let script = temp.path().join("stalled-helper");
        // This fixture accepts a private pipe but never writes a reply. Each
        // shell blocks in its own builtin, so there are no descendant processes.
        fs::write(
            &script,
            r#"#!/bin/sh
echo "$$" > "$0.$$"
IFS= read -r line
"#,
        )
        .unwrap();
        fs::set_permissions(&script, fs::Permissions::from_mode(0o700)).unwrap();
        let pool = Arc::new(Pool {
            path: script,
            idle: Default::default(),
            permits: Arc::new(Semaphore::new(4)),
        });
        let mut calls = Vec::new();
        for _ in 0..4 {
            let p = pool.clone();
            calls.push(tokio::spawn(async move {
                p.call(
                    "system.ready",
                    ["service".into(), "test".into()],
                    Value::Null,
                )
                .await
            }));
        }
        let pids = timeout(Duration::from_secs(5), async {
            loop {
                let pids = fs::read_dir(temp.path())
                    .unwrap()
                    .filter_map(|p| {
                        fs::read_to_string(p.ok()?.path())
                            .ok()?
                            .trim()
                            .parse::<u32>()
                            .ok()
                    })
                    .collect::<Vec<_>>();
                if pids.len() == 4 {
                    break pids;
                }
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
        })
        .await
        .unwrap();
        for c in calls {
            c.abort();
            let _ = c.await;
        }
        let _all = timeout(
            Duration::from_secs(5),
            pool.permits.clone().acquire_many_owned(4),
        )
        .await
        .unwrap()
        .unwrap();
        for pid in pids {
            assert!(
                !std::process::Command::new("kill")
                    .args(["-0", &pid.to_string()])
                    .stderr(Stdio::null())
                    .status()
                    .unwrap()
                    .success(),
                "stalled native child survived cancellation"
            );
        }
    }
}
