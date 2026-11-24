// SPDX-License-Identifier: MPL-2.0
use super::super::{ApiJson, Shared, WebError, WebResult};
use axum::{
    Json,
    extract::{Path, State},
    http::{HeaderMap, StatusCode},
};
use serde::Deserialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};

fn positive(v: &Value) -> Option<i64> {
    v.as_i64().filter(|n| *n > 0)
}
fn digest(v: &str, n: usize) -> bool {
    v.len() == n
        && v.bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
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
    let p: Value = serde_json::from_slice(body).map_err(|_| invalid())?;
    let Some(input) = projection(event, &p) else {
        return Ok(StatusCode::ACCEPTED);
    };
    let installation = positive(&p["installation"]["id"]).ok_or_else(invalid)?;
    let repository = positive(&p["repository"]["id"]).ok_or_else(invalid)?;
    let mut input = input;
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

fn projection(event: &str, p: &Value) -> Option<Value> {
    match event {
        "deployment_status"
            if p["action"] == "created" && p["deployment_status"]["state"].is_string() =>
        {
            let d = &p["deployment"];
            let s = &p["deployment_status"];
            let payload = match d["payload"].as_str() {
                Some(t) if t.len() < 4096 => serde_json::from_str(t).unwrap_or(Value::Null),
                _ => d["payload"].clone(),
            };
            let environment = s["environment"]
                .as_str()
                .filter(|s| !s.is_empty())
                .or_else(|| d["environment"].as_str())?;
            Some(
                json!({"kind":"deployment","positive":s["state"]=="success","deployment":positive(&d["id"])?,"status":positive(&s["id"])?,
                "creator":positive(&d["creator"]["id"])?,"deployer":positive(&s["creator"]["id"])?,
                "candidate":d["sha"].as_str()?,"at":s["created_at"].as_str()?,"environment":environment,
                "build":payload["bugsnitchBuild"].as_str().filter(|v|digest(v,64)).unwrap_or("")}),
            )
        }
        "check_run"
            if p["action"] == "completed"
                || (p["action"] == "created" && p["check_run"]["status"] == "completed") =>
        {
            let c = &p["check_run"];
            // A project verifier writes this small correlation record in the GitHub external_id.
            let correlation = c["external_id"].as_str()?;
            if let Some(monitor) = correlation.strip_prefix("bugsnitch/fix/v1 ") {
                if !digest(monitor, 32) || !c["conclusion"].is_string() {
                    return None;
                }
                return Some(
                    json!({"kind":"fix","monitor":monitor,"run":positive(&c["id"])?,"app":positive(&c["app"]["id"])?,"check":c["name"].as_str()?,
                    "candidate":c["head_sha"].as_str()?,"at":c["completed_at"].as_str()?,"positive":c["conclusion"]=="success"}),
                );
            }
            let external = correlation.strip_prefix("bugsnitch/v1 ")?;
            if external.len() > 500 {
                return None;
            }
            let proof: Value = serde_json::from_str(external).ok()?;
            let conclusion = c["conclusion"].as_str()?;
            let monitor = proof["monitor"].as_str()?;
            let build = proof["build"].as_str()?;
            if !digest(monitor, 32) || !digest(build, 64) {
                return None;
            }
            Some(
                json!({"kind":"validation","monitor":monitor,"build":build,"deployment":positive(&proof["deployment"])?,
                "status":positive(&proof["status"])?,"run":positive(&c["id"])?,"app":positive(&c["app"]["id"])?,
                "check":c["name"].as_str()?,"candidate":c["head_sha"].as_str()?,"started":c["started_at"].as_str()?,
                "at":c["completed_at"].as_str()?,"positive":conclusion=="success","inconclusive":!matches!(conclusion,"success"|"failure"),"coverage":proof["coverage"].as_bool(),"exposures":proof["exposures"].as_i64()}),
            )
        }
        _ => None,
    }
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
