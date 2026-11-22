// SPDX-License-Identifier: MPL-2.0
use super::*;
use crate::coord::{Db, Error, Result as DbResult};
use axum::{
    extract::State,
    http::{HeaderMap, header},
    middleware::Next,
    response::Response,
};
use serde::Deserialize;
use subtle::ConstantTimeEq;

const COOKIE: &str = "bugsnitch_session";
#[derive(Clone)]
pub struct Owner {
    pub workspace: String,
    pub account: String,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct Login {
    #[serde(alias = "Token")]
    token: String,
}
pub(super) async fn login(
    State(s): State<Shared>,
    ApiJson(b): ApiJson<Login>,
) -> WebResult<Response> {
    if !bool::from(s.owner_hash.as_bytes().ct_eq(hash(&b.token).as_bytes())) {
        return Err(WebError(
            StatusCode::UNAUTHORIZED,
            "Invalid owner credential".into(),
        ));
    }
    let token = random_hex(32)?;
    s.coord("session.create", service_actor(), json!(hash(&token)))
        .await?;
    Ok(cookie(&s, &token, true))
}
fn cookie(s: &AppState, token: &str, active: bool) -> Response {
    let secure = if s.origin.starts_with("https://") {
        "; Secure"
    } else {
        ""
    };
    let value = format!(
        "{COOKIE}={token}; Path=/; Max-Age={}; HttpOnly; SameSite=Strict{secure}",
        if active { 43200 } else { 0 }
    );
    (
        [(header::SET_COOKIE, value)],
        Json(json!({"authenticated":active})),
    )
        .into_response()
}
pub(super) fn session_hash(headers: &HeaderMap) -> WebResult<String> {
    let token = headers
        .get_all(header::COOKIE)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|s| s.split(';'))
        .filter_map(|s| s.trim().split_once('='))
        .find(|(k, _)| *k == COOKIE)
        .map(|(_, v)| v)
        .ok_or_else(|| WebError(StatusCode::UNAUTHORIZED, "Sign in to Bugsnitch".into()))?;
    Ok(hash(token))
}
pub(super) async fn authenticate(
    State(s): State<Shared>,
    mut r: Request,
    next: Next,
) -> WebResult<Response> {
    let record = s
        .coord(
            "session.lookup",
            service_actor(),
            json!(session_hash(r.headers())?),
        )
        .await?;
    let Some(workspace) = record["workspace"].as_str() else {
        return Err(WebError(StatusCode::UNAUTHORIZED, "Session expired".into()));
    };
    let account = record["account"].as_str().ok_or_else(WebError::internal)?;
    r.extensions_mut().insert(Owner {
        workspace: workspace.into(),
        account: account.into(),
    });
    Ok(next.run(r).await)
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct Empty {}
pub(super) async fn logout(
    State(s): State<Shared>,
    axum::Extension(o): axum::Extension<Owner>,
    headers: HeaderMap,
    ApiJson(_): ApiJson<Empty>,
) -> WebResult<Response> {
    s.coord(
        "session.delete",
        ["owner".into(), o.account],
        json!({"workspace":o.workspace,"hash":session_hash(&headers)?}),
    )
    .await?;
    Ok(cookie(&s, "", false))
}
pub(crate) fn apply(db: &Db<'_>, op: &str, input: Value) -> DbResult<Value> {
    match op{
        "session.create"=>{db.exec("INSERT INTO sessions(token_hash,workspace_id,expires_at) VALUES($1,'personal',now()+interval '12 hours')",&[input.as_str().ok_or_else(Error::internal)?])?;Ok(Value::Null)}
        "session.lookup"=>Ok(match db.optional("SELECT w.id,w.account_id FROM sessions s JOIN workspaces w ON w.id=s.workspace_id WHERE s.token_hash=$1 AND s.expires_at>now()",&[input.as_str().ok_or_else(Error::internal)?])? {Some(r)=>json!({"workspace":r.text(0)?,"account":r.text(1)?}),None=>Value::Null}),
        "session.delete"=>{db.exec("DELETE FROM sessions WHERE token_hash=$1 AND workspace_id=$2",&[input["hash"].as_str().ok_or_else(Error::internal)?,input["workspace"].as_str().ok_or_else(Error::internal)?])?;Ok(Value::Null)}
        _=>Err(Error::bad("Unknown session operation"))
    }
}
