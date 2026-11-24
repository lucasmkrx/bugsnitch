// SPDX-License-Identifier: MPL-2.0
use super::*;
use crate::coord::{Db, Error, Result as DbResult};
use axum::{
    Extension,
    extract::{Path, State},
    http::{HeaderMap, header},
    middleware::Next,
    response::Response,
    routing::{get, post},
};
use serde::Deserialize;
use subtle::ConstantTimeEq;

const COOKIE: &str = "bugsnitch_session";
#[derive(Clone)]
pub struct Owner {
    pub workspace: String,
    pub account: String,
    pub role: String,
}
impl Owner {
    pub(super) fn actor(&self) -> [String; 2] {
        [self.role.clone(), self.account.clone()]
    }
}

pub(super) async fn require_owner(r: Request, next: Next) -> WebResult<Response> {
    if r.extensions()
        .get::<Owner>()
        .is_some_and(|o| o.role == "owner")
    {
        Ok(next.run(r).await)
    } else {
        Err(WebError(
            StatusCode::FORBIDDEN,
            "Owner access required".into(),
        ))
    }
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
    let credential = hash(&b.token);
    let owner = bool::from(s.owner_hash.as_bytes().ct_eq(credential.as_bytes()));
    if !owner && (b.token.len() != 64 || !crate::devices::lower_hex(&b.token)) {
        return Err(WebError(
            StatusCode::UNAUTHORIZED,
            "Invalid workspace credential".into(),
        ));
    }
    let token = random_hex(32)?;
    let session = hash(&token);
    if owner {
        s.coord("session.create", service_actor(), json!(session))
            .await?;
    } else {
        s.coord(
            "session.member_login",
            service_actor(),
            json!({"session":session,"credential":credential}),
        )
        .await?;
    }
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
    let role = record["role"].as_str().ok_or_else(WebError::internal)?;
    r.extensions_mut().insert(Owner {
        workspace: workspace.into(),
        account: account.into(),
        role: role.into(),
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
        service_actor(),
        json!({"workspace":o.workspace,"hash":session_hash(&headers)?}),
    )
    .await?;
    Ok(cookie(&s, "", false))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Member {
    name: String,
    role: String,
    #[serde(default, rename = "projectId")]
    project: Option<String>,
}

pub(super) fn member_routes() -> Router<Shared> {
    Router::new()
        .route("/api/v1/members", get(members).post(add_member))
        .route("/api/v1/members/{id}/revoke", post(revoke_member))
}

async fn members(
    State(s): State<Shared>,
    Extension(o): Extension<Owner>,
) -> WebResult<Json<Value>> {
    s.coord(
        "session.members",
        o.actor(),
        json!({"workspace":o.workspace}),
    )
    .await
    .map(Json)
}

async fn add_member(
    State(s): State<Shared>,
    Extension(o): Extension<Owner>,
    ApiJson(b): ApiJson<Member>,
) -> WebResult<(StatusCode, Json<Value>)> {
    let name = b.name.trim();
    if b.project
        .as_ref()
        .is_some_and(|id| id.len() != 32 || !crate::devices::lower_hex(id))
        || name.is_empty()
        || name.chars().count() > 100
        || name.chars().any(char::is_control)
        || !matches!(b.role.as_str(), "operator" | "approver")
    {
        return Err(WebError(
            StatusCode::BAD_REQUEST,
            "Choose a name and an operator or approver role".into(),
        ));
    }
    let credential = random_hex(32)?;
    let id = random_hex(16)?;
    s.coord("session.add_member", o.actor(), json!({
        "workspace":o.workspace,"id":id,"name":name,"role":b.role,"project":b.project,"credential":hash(&credential),
    })).await?;
    Ok((
        StatusCode::CREATED,
        Json(json!({"id":id,"credential":credential})),
    ))
}

async fn revoke_member(
    State(s): State<Shared>,
    Extension(o): Extension<Owner>,
    Path(id): Path<String>,
    ApiJson(_): ApiJson<Empty>,
) -> WebResult<Json<Value>> {
    s.coord(
        "session.revoke_member",
        o.actor(),
        json!({"workspace":o.workspace,"id":id}),
    )
    .await
    .map(Json)
}

fn field<'a>(input: &'a Value, key: &str) -> DbResult<&'a str> {
    input[key].as_str().ok_or_else(Error::internal)
}

pub(crate) fn apply(db: &Db<'_>, op: &str, input: Value) -> DbResult<Value> {
    if matches!(
        op,
        "session.members" | "session.add_member" | "session.revoke_member"
    ) {
        let workspace = field(&input, "workspace")?;
        let (role, account) = db.actor();
        if role != "owner"
            || db
                .optional(
                    "SELECT 1 FROM workspaces WHERE id=$1 AND account_id=$2",
                    &[workspace, account],
                )?
                .is_none()
        {
            return Err(Error::denied());
        }
        return match op {
            "session.members" => {
                let row = db.one("SELECT coalesce(json_agg(json_build_object('id',id,'name',name,'role',role,'projectId',project_id,'revokedAt',revoked_at)
                    ORDER BY revoked_at NULLS FIRST,created_at,id),'[]') FROM (
                    SELECT id,name,role,project_id,revoked_at,created_at FROM workspace_members WHERE workspace_id=$1
                    ORDER BY revoked_at NULLS FIRST,created_at DESC,id LIMIT 200) members", &[workspace])?;
                Ok(serde_json::from_str(row.text(0)?)?)
            }
            "session.add_member" => {
                let id = field(&input, "id")?;
                let project = input["project"].as_str().unwrap_or("");
                if !project.is_empty()
                    && db
                        .optional(
                            "SELECT 1 FROM projects WHERE id=$1 AND workspace_id=$2",
                            &[project, workspace],
                        )?
                        .is_none()
                {
                    return Err(Error::missing());
                }
                db.exec("INSERT INTO workspace_members(workspace_id,id,name,role,token_hash,project_id) VALUES($1,$2,$3,$4,$5,NULLIF($6,''))",
                    &[workspace,id,field(&input,"name")?,field(&input,"role")?,field(&input,"credential")?,project])?;
                db.event(
                    workspace,
                    "",
                    "member.added",
                    id,
                    (role, account),
                    crate::coord::Details::default(),
                )?;
                Ok(Value::Null)
            }
            _ => {
                let id = field(&input, "id")?;
                let member = db.optional("SELECT revoked_at IS NOT NULL FROM workspace_members WHERE workspace_id=$1 AND id=$2 FOR UPDATE", &[workspace,id])?
                    .ok_or_else(Error::missing)?;
                if !member.bool(0)? {
                    db.exec("UPDATE workspace_members SET revoked_at=now() WHERE workspace_id=$1 AND id=$2", &[workspace,id])?;
                    db.exec(
                        "DELETE FROM sessions WHERE workspace_id=$1 AND member_id=$2",
                        &[workspace, id],
                    )?;
                    db.event(
                        workspace,
                        "",
                        "member.revoked",
                        id,
                        (role, account),
                        crate::coord::Details::default(),
                    )?;
                }
                Ok(json!({"revoked":true}))
            }
        };
    }
    if db.actor() != ("service", "http-server") {
        return Err(Error::denied());
    }
    match op{
        "session.create"=>{db.exec("INSERT INTO sessions(token_hash,workspace_id,expires_at) VALUES($1,'personal',now()+interval '12 hours')",&[input.as_str().ok_or_else(Error::internal)?])?;Ok(Value::Null)}
        "session.member_login" => {
            let n = db.exec("INSERT INTO sessions(token_hash,workspace_id,expires_at,member_id)
                SELECT $1,workspace_id,now()+interval '12 hours',id FROM workspace_members
                WHERE token_hash=$2 AND revoked_at IS NULL FOR SHARE",
                &[field(&input,"session")?,field(&input,"credential")?])?;
            if n != 1 { return Err(Error::unauthenticated()); }
            Ok(Value::Null)
        }
        "session.lookup"=>Ok(match db.optional("SELECT w.id,coalesce(m.id,w.account_id),coalesce(m.role,'owner')
            FROM sessions s JOIN workspaces w ON w.id=s.workspace_id
            LEFT JOIN workspace_members m ON (m.workspace_id,m.id)=(s.workspace_id,s.member_id)
            WHERE s.token_hash=$1 AND s.expires_at>now() AND (s.member_id IS NULL OR m.revoked_at IS NULL)",
            &[input.as_str().ok_or_else(Error::internal)?])? {
                Some(r)=>json!({"workspace":r.text(0)?,"account":r.text(1)?,"role":r.text(2)?}),None=>Value::Null}),
        "session.delete"=>{db.exec("DELETE FROM sessions WHERE token_hash=$1 AND workspace_id=$2",&[input["hash"].as_str().ok_or_else(Error::internal)?,input["workspace"].as_str().ok_or_else(Error::internal)?])?;Ok(Value::Null)}
        _=>Err(Error::bad("Unknown session operation"))
    }
}
