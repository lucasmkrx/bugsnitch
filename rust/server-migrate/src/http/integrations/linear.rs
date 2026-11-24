// SPDX-License-Identifier: MPL-2.0
use super::super::{Shared, WebError, WebResult};
use axum::{
    Json,
    body::Bytes,
    extract::State,
    http::{HeaderMap, StatusCode},
};
use hmac::{Hmac, Mac};
use serde_json::{Value, json};
use sha2::Sha256;
use time::OffsetDateTime;
pub(super) async fn receive(
    State(s): State<Shared>,
    h: HeaderMap,
    body: Bytes,
) -> WebResult<Json<Value>> {
    let denied = || WebError(StatusCode::UNAUTHORIZED, "Invalid Linear delivery".into());
    let secret = &s.integrations.linear_secret;
    if secret.is_empty() {
        return Err(denied());
    }
    let sig = h
        .get("linear-signature")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| hex::decode(v).ok())
        .ok_or_else(denied)?;
    let mut mac = Hmac::<Sha256>::new_from_slice(secret.as_bytes()).map_err(|_| denied())?;
    mac.update(&body);
    mac.verify_slice(&sig).map_err(|_| denied())?;
    let v: Value = serde_json::from_slice(&body).map_err(|_| denied())?;
    let now = OffsetDateTime::now_utc().unix_timestamp_nanos() / 1_000_000;
    if !v["webhookTimestamp"]
        .as_i64()
        .is_some_and(|t| (now - i128::from(t)).abs() <= 60000)
    {
        return Err(denied());
    }
    if v["type"] != "AgentSessionEvent" || (v["action"] != "created" && v["action"] != "prompted") {
        return Ok(Json(json!({"accepted":false})));
    }
    let prompt = if v["action"] == "prompted" {
        v["agentActivity"]["id"].as_str().ok_or_else(denied)?
    } else {
        ""
    };
    let result=s.domain("linear.session",["service".into(),"verified-linear".into()],json!({"session":v["agentSession"]["id"],"issue":v["agentSession"]["issue"]["id"],"prompt":prompt})).await?;
    Ok(Json(result))
}
