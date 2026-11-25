// SPDX-License-Identifier: MPL-2.0
//! Stable public device protocol. Only authenticated credential digests and
//! named operations reach the coordinator; the server derives owner scope.
mod recommendation;

use super::{ApiJson, Owner, Shared, WebError, WebResult, hash, random_hex};
use crate::devices::lower_hex;
use axum::{
    Extension, Json, Router,
    extract::{FromRequestParts, Path, Query, State},
    http::{StatusCode, header::AUTHORIZATION, request::Parts},
    routing::{get, post},
};
use serde::Deserialize;
use serde_json::{Value, json};
use std::collections::HashMap;
use time::{Duration, OffsetDateTime, format_description::well_known::Rfc3339};

type Reply = WebResult<Json<Value>>;

pub fn public() -> Router<Shared> {
    Router::new()
        .route("/api/v1/device/pair", post(pair))
        .route("/api/v1/device/sync", post(sync))
        .route("/api/v1/device/draft-delivery", post(draft_delivery))
        .route("/api/v1/device/progression", post(progression))
        .route("/api/v1/device/progression-wake", post(progression_wake))
        .route("/api/v1/device/rotate", post(rotate))
        .route("/api/v1/device/admission", post(admission))
        .route("/api/v1/device/model-permit", post(permit))
        .route("/api/v1/device/worker-lease", post(lease))
        .route("/api/v1/device/next", post(next))
        .route("/api/v1/device/share-diff", post(share_diff))
}

pub fn owner() -> Router<Shared> {
    Router::new()
        .route("/api/v1/pairings", post(pairing))
        .route("/api/v1/devices/{id}/revoke", post(revoke))
        .route("/api/v1/attempts/{id}/control", post(control))
        .route("/api/v1/attempts/{id}/ticket-decision", post(ticket_answer))
        .route("/api/v1/workspace/dispatch", post(workspace_control))
        .route("/api/v1/events", get(events))
        .route("/api/v1/attempts/{id}/diff", get(read_diff))
}

fn bad(message: &str) -> WebError {
    WebError(StatusCode::BAD_REQUEST, message.into())
}
fn invalid() -> WebError {
    WebError(StatusCode::INTERNAL_SERVER_ERROR, "Request failed".into())
}

fn authenticated(owner: &Owner) -> [String; 2] {
    ["owner".into(), owner.account.clone()]
}
// Each device transaction resolves its audit actor after authenticating the hash.
fn device_actor() -> [String; 2] {
    [String::new(), String::new()]
}

struct Device(String);
impl<S: Send + Sync> FromRequestParts<S> for Device {
    type Rejection = WebError;
    async fn from_request_parts(parts: &mut Parts, _: &S) -> WebResult<Self> {
        let value = parts
            .headers
            .get(AUTHORIZATION)
            .and_then(|h| h.to_str().ok())
            .and_then(|s| s.strip_prefix("Bearer "));
        value
            .filter(|s| s.len() == 64 && lower_hex(s))
            .map(|s| Self(hash(s)))
            .ok_or_else(|| WebError(StatusCode::FORBIDDEN, "Permission denied".into()))
    }
}
fn compatible(protocol: i64) -> WebResult<()> {
    if protocol == 1 {
        Ok(())
    } else {
        Err(WebError(
            StatusCode::UPGRADE_REQUIRED,
            "Incompatible runner protocol. Install a version supporting device protocol 1.".into(),
        ))
    }
}
fn version(body: &Value) -> WebResult<()> {
    if !body.is_object() {
        return Err(bad("Invalid JSON request"));
    }
    let value = match body.get("protocol") {
        None | Some(Value::Null) => 0,
        Some(v) => v.as_i64().ok_or_else(|| bad("Invalid JSON request"))?,
    };
    compatible(value)
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Pairing {
    #[serde(alias = "Name")]
    name: String,
    #[serde(alias = "ProjectIDs", alias = "projectIDs")]
    project_ids: Vec<String>,
}
async fn pairing(
    State(s): State<Shared>,
    Extension(owner): Extension<Owner>,
    ApiJson(b): ApiJson<Pairing>,
) -> WebResult<(StatusCode, Json<Value>)> {
    let name = b.name.trim();
    if name.is_empty() || name.len() > 100 || !(1..=40).contains(&b.project_ids.len()) {
        return Err(bad("Choose a device name and one to forty projects"));
    }
    let code = random_hex(8)?;
    let expiry = (OffsetDateTime::now_utc() + Duration::minutes(10))
        .format(&Rfc3339)
        .map_err(|_| invalid())?;
    s.coord(
        "device.pairing",
        authenticated(&owner),
        json!({"workspace":owner.workspace, "name":name,
        "projects":b.project_ids, "codeHash":hash(&code), "expiresAt":expiry}),
    )
    .await?;
    Ok((
        StatusCode::CREATED,
        Json(json!({"code":code,"expiresAt":expiry})),
    ))
}

#[derive(Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
struct Pair {
    protocol: i64,
    code: String,
    credential: String,
    platform: String,
}
async fn pair(State(s): State<Shared>, ApiJson(b): ApiJson<Pair>) -> Reply {
    compatible(b.protocol)?;
    if b.code.len() != 16
        || b.credential.len() != 64
        || !lower_hex(&b.credential)
        || !matches!(
            b.platform.split_once('/'),
            Some(("darwin" | "windows" | "linux", "amd64" | "arm64"))
        )
    {
        return Err(bad("Invalid pairing payload"));
    }
    let out = s
        .coord(
            "device.redeem",
            device_actor(),
            json!({"codeHash":hash(&b.code),"credentialHash":hash(&b.credential),
        "platform":b.platform,"newId":random_hex(16)?}),
        )
        .await?;
    Ok(Json(out))
}
#[derive(Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
struct Rotate {
    protocol: i64,
    credential: String,
}
async fn rotate(
    State(s): State<Shared>,
    Device(current): Device,
    ApiJson(b): ApiJson<Rotate>,
) -> Reply {
    compatible(b.protocol)?;
    let next = hash(&b.credential);
    if current == next || b.credential.len() != 64 || !lower_hex(&b.credential) {
        return Err(bad("Provide a fresh device credential"));
    }
    Ok(Json(
        s.coord(
            "device.rotate",
            device_actor(),
            json!({"currentHash":current,"nextHash":next}),
        )
        .await?,
    ))
}

async fn sync(
    State(s): State<Shared>,
    Device(token): Device,
    ApiJson(body): ApiJson<Value>,
) -> Reply {
    version(&body)?;
    Ok(Json(
        s.coord(
            "device.sync",
            device_actor(),
            json!({"credentialHash":token,"sync":body}),
        )
        .await?,
    ))
}

async fn claim(s: Shared, token: String, op: &str, body: Value) -> WebResult<Value> {
    version(&body)?;
    s.coord(
        op,
        device_actor(),
        json!({"tokenHash":token,"request":body}),
    )
    .await
}
async fn draft_delivery(
    State(s): State<Shared>,
    Device(token): Device,
    ApiJson(body): ApiJson<Value>,
) -> Reply {
    Ok(Json(claim(s, token, "device.draft_delivery", body).await?))
}

async fn progression(
    State(s): State<Shared>,
    Device(token): Device,
    ApiJson(body): ApiJson<Value>,
) -> Reply {
    let mut action = claim(s.clone(), token, "device.progression", body).await?;
    if action["deployment"].is_array() {
        let statuses=s.domain("progression.verdict",["service".into(),"device-progression".into()],
            json!({"workspace":action["workspaceId"],"project":action["projectId"],"deployments":action["deployment"]})).await?;
        action["deployment"] = statuses;
    }
    Ok(Json(action))
}

async fn progression_wake(
    State(s): State<Shared>,
    Device(token): Device,
    ApiJson(body): ApiJson<Value>,
) -> Reply {
    let after = body["after"]
        .as_i64()
        .filter(|n| *n >= 0)
        .ok_or_else(|| bad("Invalid progression cursor"))?;
    let project = body["projectId"]
        .as_str()
        .ok_or_else(|| bad("Invalid progression project"))?;
    let cursor = claim(
        s.clone(),
        token.clone(),
        "device.progression_cursor",
        body.clone(),
    )
    .await?;
    if cursor["position"].as_i64().is_some_and(|n| n > after) {
        return Ok(Json(cursor));
    }
    s.domain(
        "progression.wait",
        ["service".into(), "device-progression".into()],
        json!({"project":project}),
    )
    .await?;
    Ok(Json(
        claim(s, token, "device.progression_cursor", body).await?,
    ))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct TicketAnswer {
    version: i64,
    answer: String,
}

async fn ticket_answer(
    State(s): State<Shared>,
    Extension(o): Extension<Owner>,
    Path(id): Path<String>,
    ApiJson(b): ApiJson<TicketAnswer>,
) -> Reply {
    Ok(Json(
        s.coord(
            "device.ticket_answer",
            o.actor(),
            json!({
                "workspace":o.workspace,"attemptId":id,"version":b.version,"answer":b.answer
            }),
        )
        .await?,
    ))
}

async fn admission(
    State(s): State<Shared>,
    Device(token): Device,
    ApiJson(body): ApiJson<Value>,
) -> Reply {
    Ok(Json(claim(s, token, "claims.admission", body).await?))
}
async fn permit(
    State(s): State<Shared>,
    Device(token): Device,
    ApiJson(body): ApiJson<Value>,
) -> Reply {
    Ok(Json(claim(s, token, "claims.permit", body).await?))
}
async fn lease(
    State(s): State<Shared>,
    Device(token): Device,
    ApiJson(body): ApiJson<Value>,
) -> Reply {
    Ok(Json(claim(s, token, "claims.lease", body).await?))
}
async fn next(
    State(s): State<Shared>,
    Device(token): Device,
    ApiJson(body): ApiJson<Value>,
) -> Reply {
    let snapshot = claim(s, token, "claims.next", body).await?;
    Ok(Json(recommendation::project(snapshot)?))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Empty {}
async fn revoke(
    State(s): State<Shared>,
    Extension(owner): Extension<Owner>,
    Path(id): Path<String>,
    ApiJson(_): ApiJson<Empty>,
) -> Reply {
    s.coord(
        "device.revoke",
        authenticated(&owner),
        json!({"workspace":owner.workspace,"device":id}),
    )
    .await?;
    Ok(Json(json!({"revoked":true})))
}
async fn control(
    State(s): State<Shared>,
    Extension(owner): Extension<Owner>,
    Path(id): Path<String>,
    ApiJson(body): ApiJson<Value>,
) -> WebResult<(StatusCode, Json<Value>)> {
    let out=s.coord("device.control",authenticated(&owner),json!({"workspace":owner.workspace,"attempt":id,"request":body,"newId":random_hex(16)?})).await?;
    Ok((StatusCode::ACCEPTED, Json(out)))
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct WorkspaceControl {
    #[serde(alias = "Stopped")]
    stopped: bool,
    #[serde(alias = "Version")]
    version: i64,
    #[serde(alias = "RequestID")]
    request_id: String,
}
async fn workspace_control(
    State(s): State<Shared>,
    Extension(owner): Extension<Owner>,
    ApiJson(b): ApiJson<WorkspaceControl>,
) -> Reply {
    Ok(Json(s.coord("device.workspace_control",authenticated(&owner),json!({"workspace":owner.workspace,"stopped":b.stopped,"version":b.version,"requestId":b.request_id})).await?))
}
async fn events(
    State(s): State<Shared>,
    Extension(owner): Extension<Owner>,
    Query(q): Query<HashMap<String, String>>,
) -> Reply {
    let after = q
        .get("after")
        .map(String::as_str)
        .unwrap_or("0")
        .parse::<i64>()
        .ok()
        .filter(|v| *v >= 0)
        .ok_or_else(|| bad("Use a nonnegative event cursor"))?;
    let limit = q
        .get("limit")
        .map(String::as_str)
        .unwrap_or("50")
        .parse::<usize>()
        .ok()
        .filter(|v| (1..=150).contains(v))
        .ok_or_else(|| bad("Event page size must be between 1 and 150"))?;
    Ok(Json(
        s.coord(
            "events.replay",
            authenticated(&owner),
            json!({"workspace":owner.workspace,"after":after,"limit":limit}),
        )
        .await?,
    ))
}
#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Diff {
    #[serde(default)]
    protocol: i64,
    attempt_id: String,
    version: i64,
    base_sha: String,
    candidate_sha: String,
    text: String,
    digest: String,
}
async fn share_diff(
    State(s): State<Shared>,
    Device(token): Device,
    ApiJson(b): ApiJson<Diff>,
) -> Reply {
    compatible(b.protocol)?;
    Ok(Json(
        s.coord(
            "device.share_diff",
            device_actor(),
            json!({"credentialHash":token,"attemptId":b.attempt_id,"version":b.version,
        "baseSha":b.base_sha,"candidateSha":b.candidate_sha,"text":b.text,"digest":b.digest}),
        )
        .await?,
    ))
}
async fn read_diff(
    State(s): State<Shared>,
    Extension(owner): Extension<Owner>,
    Path(id): Path<String>,
) -> Reply {
    Ok(Json(
        s.coord(
            "device.read_diff",
            authenticated(&owner),
            json!({"workspace":owner.workspace,"attempt":id}),
        )
        .await?,
    ))
}
