// SPDX-License-Identifier: MPL-2.0
use super::super::{Shared, WebError, WebResult};
use axum::{
    Json,
    body::Bytes,
    extract::State,
    http::{HeaderMap, StatusCode},
};
use hmac::{Hmac, KeyInit, Mac};
use serde::Deserialize;
use serde_json::{Value, json};
use sha2::Sha256;
use time::OffsetDateTime;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct LinearEvent {
    #[serde(rename = "type")]
    kind: String,
    action: String,
    webhook_timestamp: i64,
    agent_session: Option<AgentSession>,
    agent_activity: Option<LinearReference>,
}

#[derive(Deserialize)]
struct AgentSession {
    id: String,
    issue: LinearReference,
}

#[derive(Deserialize)]
struct LinearReference {
    id: String,
}

fn invalid_delivery() -> WebError {
    WebError(StatusCode::UNAUTHORIZED, "Invalid Linear delivery".into())
}

fn session_input(body: &[u8], now_ms: i128) -> WebResult<Option<Value>> {
    let Json(event) = Json::<LinearEvent>::from_bytes(body).map_err(|_| invalid_delivery())?;
    if (now_ms - i128::from(event.webhook_timestamp)).abs() > 60_000 {
        return Err(invalid_delivery());
    }
    if event.kind != "AgentSessionEvent" || !matches!(event.action.as_str(), "created" | "prompted")
    {
        return Ok(None);
    }
    let session = event.agent_session.ok_or_else(invalid_delivery)?;
    let valid_id = |id: &str| super::hooks::UUID.is_match(id);
    if !valid_id(&session.id) || !valid_id(&session.issue.id) {
        return Err(invalid_delivery());
    }
    let prompt = if event.action == "prompted" {
        event
            .agent_activity
            .map(|a| a.id)
            .filter(|id| valid_id(id))
            .ok_or_else(invalid_delivery)?
    } else {
        String::new()
    };
    Ok(Some(
        json!({"session":session.id,"issue":session.issue.id,"prompt":prompt}),
    ))
}

pub(super) async fn receive(
    State(s): State<Shared>,
    h: HeaderMap,
    body: Bytes,
) -> WebResult<Json<Value>> {
    let secret = &s.integrations.linear_secret;
    if secret.is_empty() {
        return Err(invalid_delivery());
    }
    let sig = h
        .get("linear-signature")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| hex::decode(v).ok())
        .ok_or_else(invalid_delivery)?;
    let mut mac =
        Hmac::<Sha256>::new_from_slice(secret.as_bytes()).map_err(|_| invalid_delivery())?;
    mac.update(&body);
    mac.verify_slice(&sig).map_err(|_| invalid_delivery())?;
    let now = OffsetDateTime::now_utc().unix_timestamp_nanos() / 1_000_000;
    let Some(input) = session_input(&body, now)? else {
        return Ok(Json(json!({"accepted":false})));
    };
    let result = s
        .domain(
            "linear.session",
            ["service".into(), "verified-linear".into()],
            input,
        )
        .await?;
    Ok(Json(result))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn linear_session_projects_only_verified_ids_and_checks_prompt_shape() {
        let issue = "11111111-1111-4111-8111-111111111111";
        let session = "22222222-2222-4222-8222-222222222222";
        let prompt = "33333333-3333-4333-8333-333333333333";
        let mut event = json!({
            "type": "AgentSessionEvent", "action": "prompted", "webhookTimestamp": 12345,
            "agentActivity": {"id":prompt,"body":"private"},
            "agentSession": {"id":session,"issue":{"id":issue,"title":"private"}}
        });
        let project = |event: &Value| session_input(&serde_json::to_vec(event).unwrap(), 12345);
        assert_eq!(
            project(&event).unwrap(),
            Some(json!({"session":session,"issue":issue,"prompt":prompt}))
        );
        event["agentActivity"]["id"] = json!("not-a-uuid");
        assert!(project(&event).is_err());
        event["action"] = json!("created");
        assert_eq!(project(&event).unwrap().unwrap()["prompt"], "");
        event["agentSession"]["issue"]["id"] = json!(7);
        assert!(project(&event).is_err());
        assert!(
            project(&json!({"type":"Issue","action":"updated","webhookTimestamp":12345}))
                .unwrap()
                .is_none()
        );
    }
}
