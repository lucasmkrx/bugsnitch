// SPDX-License-Identifier: MPL-2.0
use super::super::{ApiJson, Shared, WebError, WebResult};
use axum::{
    Json,
    extract::{Path, State},
    http::{HeaderMap, StatusCode},
};
use serde::Deserialize;
use serde_json::{Value, json, value::RawValue};
use sha2::{Digest, Sha256};

#[derive(Deserialize)]
struct GitHubObject {
    id: i64,
}

#[derive(Deserialize)]
struct ReleaseEvent {
    action: String,
    installation: Option<GitHubObject>,
    repository: Option<GitHubObject>,
    deployment: Option<Deployment>,
    deployment_status: Option<DeploymentStatus>,
    check_run: Option<CheckRun>,
}

#[derive(Deserialize)]
struct Deployment {
    id: i64,
    creator: Option<GitHubObject>,
    sha: String,
    environment: Option<String>,
    payload: Option<Box<RawValue>>,
}

#[derive(Deserialize)]
struct DeploymentStatus {
    id: i64,
    creator: Option<GitHubObject>,
    state: String,
    environment: Option<String>,
    created_at: String,
}

#[derive(Deserialize)]
struct BuildMetadata {
    #[serde(rename = "bugsnitchBuild")]
    digest: String,
}

// GitHub accepts either a payload object or a string containing that object.
fn build_digest(payload: Option<&RawValue>) -> Option<String> {
    let raw = payload?.get();
    let decoded;
    let object = if raw.starts_with('"') {
        decoded = Json::<String>::from_bytes(raw.as_bytes()).ok()?.0;
        if decoded.len() >= 4096 {
            return None;
        }
        decoded.trim_start()
    } else {
        raw
    };
    if !object.starts_with('{') {
        return None;
    }
    let Json(metadata) = Json::<BuildMetadata>::from_bytes(object.as_bytes()).ok()?;
    digest(&metadata.digest, 64).then_some(metadata.digest)
}

#[derive(Deserialize)]
struct CheckRun {
    id: i64,
    app: Option<GitHubObject>,
    external_id: Option<String>,
    status: Option<String>,
    conclusion: Option<String>,
    name: String,
    head_sha: String,
    started_at: Option<String>,
    completed_at: Option<String>,
}

#[derive(Deserialize)]
struct CheckProof {
    monitor: String,
    build: String,
    deployment: i64,
    status: i64,
    coverage: Option<bool>,
    exposures: Option<i64>,
}

fn digest(v: &str, n: usize) -> bool {
    v.len() == n && crate::devices::lower_hex(v)
}

fn positive(id: i64) -> Option<i64> {
    (id > 0).then_some(id)
}

pub(super) async fn record(
    state: Shared,
    delivery: &str,
    event: &str,
    body: &[u8],
) -> WebResult<StatusCode> {
    let invalid = || {
        WebError(
            StatusCode::UNPROCESSABLE_ENTITY,
            "Invalid release signal".into(),
        )
    };
    let Json(payload) = Json::<ReleaseEvent>::from_bytes(body).map_err(|_| invalid())?;
    let Some(mut input) = projection(event, &payload) else {
        return Ok(StatusCode::ACCEPTED);
    };
    let installation = payload
        .installation
        .and_then(|r| positive(r.id))
        .ok_or_else(invalid)?;
    let repository = payload
        .repository
        .and_then(|r| positive(r.id))
        .ok_or_else(invalid)?;
    input["delivery"] = json!(delivery);
    input["installation"] = json!(installation);
    input["repository"] = json!(repository);
    state
        .domain(
            "deployment.github",
            ["provider".into(), format!("github/{installation}")],
            input,
        )
        .await?;
    Ok(StatusCode::ACCEPTED)
}

fn projection(event: &str, payload: &ReleaseEvent) -> Option<Value> {
    match event {
        "deployment_status" if payload.action == "created" => {
            let deployment = payload.deployment.as_ref()?;
            let status = payload.deployment_status.as_ref()?;
            if !crate::devices::valid_sha(&deployment.sha) {
                return None;
            }
            let environment = status
                .environment
                .as_deref()
                .filter(|s| !s.is_empty())
                .or(deployment.environment.as_deref())?;
            Some(json!({
                "kind": "deployment",
                "positive": status.state == "success",
                "deployment": positive(deployment.id)?,
                "status": positive(status.id)?,
                "creator": positive(deployment.creator.as_ref()?.id)?,
                "deployer": positive(status.creator.as_ref()?.id)?,
                "candidate": deployment.sha,
                "at": status.created_at,
                "environment": environment,
                "build": build_digest(deployment.payload.as_deref()).unwrap_or_default(),
            }))
        }
        "check_run" => {
            let check = payload.check_run.as_ref()?;
            if !(payload.action == "completed"
                || (payload.action == "created" && check.status.as_deref() == Some("completed")))
            {
                return None;
            }
            check_projection(check)
        }
        _ => None,
    }
}

fn check_projection(check: &CheckRun) -> Option<Value> {
    let external = check.external_id.as_deref()?;
    let conclusion = check.conclusion.as_deref()?;
    let run = positive(check.id)?;
    let app = positive(check.app.as_ref()?.id)?;
    let at = check.completed_at.as_deref()?;
    if !crate::devices::valid_sha(&check.head_sha) {
        return None;
    }
    if let Some(monitor) = external.strip_prefix("bugsnitch/fix/v1 ") {
        return digest(monitor, 32).then(|| {
            json!({
                "kind": "fix", "monitor": monitor, "run": run, "app": app, "check": check.name,
                "candidate": check.head_sha, "at": at, "positive": conclusion == "success",
            })
        });
    }
    // An independent verifier writes this small correlation record in external_id.
    let external = external.strip_prefix("bugsnitch/v1 ")?;
    if external.len() > 500 || !external.trim_start().starts_with('{') {
        return None;
    }
    let Json(proof) = Json::<CheckProof>::from_bytes(external.as_bytes()).ok()?;
    if !digest(&proof.monitor, 32)
        || !digest(&proof.build, 64)
        || proof.exposures.is_some_and(|n| n < 0)
    {
        return None;
    }
    Some(json!({
        "kind": "validation", "monitor": proof.monitor, "build": proof.build,
        "deployment": positive(proof.deployment)?, "status": positive(proof.status)?,
        "run": run, "app": app, "check": check.name, "candidate": check.head_sha,
        "started": check.started_at.as_deref()?, "at": at, "positive": conclusion == "success",
        "inconclusive": !matches!(conclusion, "success" | "failure"),
        "coverage": proof.coverage, "exposures": proof.exposures,
    }))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct GateSubject {
    candidate: String,
    build: String,
    deployment: i64,
    status: i64,
}

pub(super) async fn gate(
    State(s): State<Shared>,
    Path(monitor): Path<String>,
    headers: HeaderMap,
    ApiJson(subject): ApiJson<GateSubject>,
) -> WebResult<(StatusCode, Json<Value>)> {
    let key = headers
        .get("authorization")
        .and_then(|h| h.to_str().ok())
        .and_then(|h| h.strip_prefix("Bearer "))
        .filter(|t| digest(t, 64))
        .ok_or_else(|| WebError(StatusCode::UNAUTHORIZED, "Gate credential required".into()))?;
    if !digest(&monitor, 32) {
        return Err(WebError(StatusCode::BAD_REQUEST, "Invalid monitor".into()));
    }
    let reply=s.domain("deployment.gate",["service".into(),"deployment-gate".into()],json!({
        "monitor":monitor,"tokenHash":hex::encode(Sha256::digest(key.as_bytes())),
        "candidate":subject.candidate,"build":subject.build,"deployment":subject.deployment,"status":subject.status
    })).await?;
    let status = if reply["allowed"] == true {
        StatusCode::OK
    } else {
        StatusCode::CONFLICT
    };
    Ok((status, Json(reply)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::value::to_raw_value;

    #[test]
    fn deployment_accepts_both_build_payload_encodings() {
        let digest = "a".repeat(64);
        let metadata = json!({"bugsnitchBuild": digest, "extra": {"ignored": true}});
        for payload in [metadata.clone(), json!(metadata.to_string())] {
            let raw = to_raw_value(&payload).unwrap();
            assert_eq!(build_digest(Some(&raw)).as_deref(), Some(digest.as_str()));
        }
        for payload in [
            json!({"bugsnitchBuild": 7}),
            json!([digest]),
            json!(format!("{}{}", " ".repeat(4096), metadata)),
        ] {
            let raw = to_raw_value(&payload).unwrap();
            assert!(build_digest(Some(&raw)).is_none());
        }
    }

    #[test]
    fn validation_requires_typed_positive_correlation_and_bounded_external_id() {
        let mut proof = json!({
            "monitor": "a".repeat(32), "build": "b".repeat(64), "deployment": 3, "status": 4,
            "exposures": 0, "coverage": false, "extra": "ignored"
        });
        let signal = |proof: &Value| {
            let event = json!({
                "action": "created", "check_run": {
                    "id": 8, "name": "checkout", "status": "completed", "app": {"id": 9},
                    "started_at": "2026-11-02T03:03:03Z", "completed_at": "2026-11-02T03:03:04Z",
                    "conclusion": "success", "head_sha": "c".repeat(40),
                    "external_id": format!("bugsnitch/v1 {proof}")
                }
            });
            let Json(parsed) =
                Json::<ReleaseEvent>::from_bytes(&serde_json::to_vec(&event).unwrap()).unwrap();
            projection("check_run", &parsed)
        };
        let projected = signal(&proof).unwrap();
        assert_eq!(projected["exposures"], 0);
        assert_eq!(projected["coverage"], false);
        proof["coverage"] = json!("false");
        assert!(signal(&proof).is_none());
        proof["coverage"] = Value::Null;
        proof["deployment"] = json!(0);
        assert!(signal(&proof).is_none());
        proof["deployment"] = json!(1);
        proof["extra"] = json!("a".repeat(501));
        assert!(signal(&proof).is_none());
    }
}
