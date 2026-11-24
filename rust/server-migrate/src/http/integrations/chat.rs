// SPDX-License-Identifier: MPL-2.0
use super::super::{Shared, WebError, WebResult};
use axum::{
    Json,
    body::Bytes,
    extract::State,
    http::{HeaderMap, StatusCode},
};
use hmac::{Hmac, Mac};
use serde::Deserialize;
use serde_json::{Value, json};
use sha2::Sha256;
use subtle::ConstantTimeEq;
use time::OffsetDateTime;

fn denied() -> WebError {
    WebError(
        StatusCode::FORBIDDEN,
        "Chat identity could not be verified".into(),
    )
}
fn header<'a>(h: &'a HeaderMap, key: &str) -> WebResult<&'a str> {
    let mut values = h.get_all(key).iter();
    let value = values
        .next()
        .and_then(|v| v.to_str().ok())
        .ok_or_else(denied)?;
    if values.next().is_some() {
        return Err(denied());
    }
    Ok(value)
}
fn fresh(timestamp: i64) -> bool {
    OffsetDateTime::now_utc()
        .unix_timestamp()
        .abs_diff(timestamp)
        <= 300
}
async fn dispatch(s: &Shared, input: Value) -> WebResult<String> {
    let v = s
        .domain(
            "chat.command",
            ["service".into(), "verified-chat".into()],
            input,
        )
        .await?;
    Ok(v.as_str().ok_or_else(WebError::internal)?.to_owned())
}
pub(super) async fn slack(
    State(s): State<Shared>,
    h: HeaderMap,
    body: Bytes,
) -> WebResult<Json<Value>> {
    let secret = &s.integrations.slack_secret;
    if secret.is_empty() {
        return Err(denied());
    }
    let ts = header(&h, "x-slack-request-timestamp")?;
    if !ts.parse::<i64>().is_ok_and(fresh) {
        return Err(denied());
    }
    let sig = header(&h, "x-slack-signature")?
        .strip_prefix("v0=")
        .and_then(|v| hex::decode(v).ok())
        .ok_or_else(denied)?;
    let mut mac = Hmac::<Sha256>::new_from_slice(secret.as_bytes()).map_err(|_| denied())?;
    mac.update(format!("v0:{ts}:").as_bytes());
    mac.update(&body);
    mac.verify_slice(&sig).map_err(|_| denied())?;
    let form: std::collections::HashMap<_, _> = url::form_urlencoded::parse(&body).collect();
    if form.get("command").map(|x| x.as_ref()) != Some("/bugsnitch") {
        return Err(denied());
    }
    let msg=dispatch(&s,json!({"provider":"slack","tenant":form.get("team_id"),"user":form.get("user_id"),"text":form.get("text")})).await?;
    Ok(Json(json!({"response_type":"ephemeral","text":msg})))
}
#[derive(Deserialize)]
struct TelegramUpdate {
    message: TelegramMessage,
}

#[derive(Deserialize)]
struct TelegramMessage {
    from: TelegramUser,
    chat: TelegramChat,
    date: i64,
    text: Option<String>,
}

#[derive(Deserialize)]
struct TelegramUser {
    id: i64,
    is_bot: bool,
}

#[derive(Deserialize)]
struct TelegramChat {
    id: i64,
    #[serde(rename = "type")]
    kind: String,
}

pub(super) async fn telegram(
    State(s): State<Shared>,
    h: HeaderMap,
    body: Bytes,
) -> WebResult<Json<Value>> {
    let secret = &s.integrations.telegram_secret;
    if secret.is_empty()
        || !bool::from(
            secret
                .as_bytes()
                .ct_eq(header(&h, "x-telegram-bot-api-secret-token")?.as_bytes()),
        )
    {
        return Err(denied());
    }
    let Json(update) = Json::<TelegramUpdate>::from_bytes(&body).map_err(|_| denied())?;
    let msg = update.message;
    let user = msg.from.id;
    if user <= 0
        || msg.from.is_bot
        || msg.chat.kind != "private"
        || msg.chat.id != user
        || !fresh(msg.date)
    {
        return Err(denied());
    }
    let Some(t) = msg.text else {
        return Ok(Json(json!({})));
    };
    let Some(args) = t.strip_prefix("/bugsnitch") else {
        return Ok(Json(json!({})));
    };
    if !args.is_empty() && !args.starts_with(' ') {
        return Ok(Json(json!({})));
    }
    let result=dispatch(&s,json!({"provider":"telegram","tenant":"private","user":user.to_string(),"text":args.trim()})).await;
    let text = match result {
        Ok(v) => v,
        Err(e) if e.0.is_client_error() => {
            "Command unavailable. Check membership, syntax, and the current decision version."
                .into()
        }
        Err(e) => return Err(e),
    };
    Ok(Json(
        json!({"method":"sendMessage","chat_id":user,"text":text}),
    ))
}
