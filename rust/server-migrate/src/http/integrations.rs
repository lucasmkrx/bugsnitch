// SPDX-License-Identifier: MPL-2.0
mod chat;
pub(crate) mod hooks;
mod linear;
pub(super) mod mcp;
mod releases;

use super::Shared;
use axum::{
    Router,
    extract::DefaultBodyLimit,
    routing::{any, get, post},
};
use reqwest::{Client, Url};
use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::{
    collections::HashSet,
    env,
    time::{Duration, SystemTime, UNIX_EPOCH},
};
use subtle::ConstantTimeEq;

const SCOPES: [&str; 6] = [
    "workspace.read",
    "incident.write",
    "decision.write",
    "plan.write",
    "feedback.write",
    "attempt.control",
];
const DISCOVERY: &str = "/.well-known/oauth-protected-resource/mcp";

pub struct Integrations {
    linear_secret: String,
    slack_secret: String,
    telegram_secret: String,
    github_secret: String,
    sentry_secret: String,
    local_hash: Option<[u8; 32]>,
    local_scopes: HashSet<String>,
    oauth: Option<OAuth>,
    client: Client,
}

struct OAuth {
    issuer: String,
    introspection: Url,
    client_id: String,
    client_secret: String,
    subject: String,
}

pub fn from_env() -> Result<Integrations, &'static str> {
    let get = |name: &str| env::var(name).unwrap_or_default();
    let issuer = get("MCP_OAUTH_ISSUER");
    let endpoint = get("MCP_OAUTH_INTROSPECTION");
    let https_url = |s: &str| {
        Url::parse(s).ok().filter(|u| {
            u.scheme() == "https"
                && u.host().is_some()
                && u.username().is_empty()
                && u.password().is_none()
                && u.fragment().is_none()
        })
    };
    let oauth = match (https_url(&issuer), https_url(&endpoint)) {
        (Some(_), Some(introspection)) => {
            let o = OAuth {
                issuer,
                introspection,
                client_id: get("MCP_OAUTH_CLIENT_ID"),
                client_secret: get("MCP_OAUTH_CLIENT_SECRET"),
                subject: get("MCP_OAUTH_OWNER_SUBJECT"),
            };
            (!o.client_id.is_empty() && !o.client_secret.is_empty() && !o.subject.is_empty())
                .then_some(o)
        }
        _ => None,
    };
    let local = get("BUGSNITCH_MCP_TOKEN");
    let scopes = get("BUGSNITCH_MCP_SCOPES");
    let scopes = if scopes.is_empty() {
        "workspace.read incident.write decision.write"
    } else {
        &scopes
    };
    Ok(Integrations {
        linear_secret: get("LINEAR_WEBHOOK_SECRET"),
        slack_secret: get("SLACK_SIGNING_SECRET"),
        telegram_secret: get("TELEGRAM_WEBHOOK_SECRET"),
        github_secret: get("GITHUB_WEBHOOK_SECRET"),
        sentry_secret: get("SENTRY_CLIENT_SECRET"),
        local_hash: (local.len() >= 32).then(|| Sha256::digest(local.as_bytes()).into()),
        local_scopes: scopes.split_ascii_whitespace().map(str::to_owned).collect(),
        client: Client::builder()
            .https_only(true)
            .redirect(reqwest::redirect::Policy::none())
            .timeout(Duration::from_secs(5))
            .build()
            .map_err(|_| "could not initialize OAuth introspection")?,
        oauth,
    })
}

#[derive(Clone)]
struct Grant {
    id: &'static str,
    scopes: HashSet<String>,
}

#[derive(Deserialize)]
struct TokenStatus {
    active: bool,
    iss: String,
    sub: String,
    scope: String,
    exp: i64,
    aud: Audience,
}
#[derive(Deserialize)]
#[serde(untagged)]
enum Audience {
    Single(String),
    Many(Vec<String>),
}

impl OAuth {
    fn grant(&self, t: TokenStatus, audience: &str, now: i64) -> Option<Grant> {
        let matches = match t.aud {
            Audience::Single(v) => v == audience,
            Audience::Many(v) => v.iter().any(|a| a == audience),
        };
        (t.active && t.iss == self.issuer && t.sub == self.subject && t.exp > now && matches).then(
            || Grant {
                id: "oauth-owner-grant",
                scopes: t
                    .scope
                    .split_ascii_whitespace()
                    .map(str::to_owned)
                    .collect(),
            },
        )
    }
}

impl Integrations {
    async fn grant(&self, token: &str, audience: &str) -> Option<Grant> {
        if token.is_empty() || token.len() > 8192 {
            return None;
        }
        if self
            .local_hash
            .as_ref()
            .is_some_and(|h| bool::from(h.ct_eq(&Sha256::digest(token.as_bytes()))))
        {
            return Some(Grant {
                id: "local-grant",
                scopes: self.local_scopes.clone(),
            });
        }
        let o = self.oauth.as_ref()?;
        let mut resp = self
            .client
            .post(o.introspection.clone())
            .basic_auth(&o.client_id, Some(&o.client_secret))
            .form(&[("token", token), ("token_type_hint", "access_token")])
            .send()
            .await
            .ok()?;
        if resp.status() != 200 || resp.content_length().is_some_and(|n| n > 65536) {
            return None;
        }
        let mut bytes = Vec::new();
        while let Some(chunk) = resp.chunk().await.ok()? {
            if chunk.len() > 65536 - bytes.len() {
                return None;
            }
            bytes.extend_from_slice(&chunk);
        }
        let status = serde_json::from_slice(&bytes).ok()?;
        o.grant(
            status,
            audience,
            SystemTime::now().duration_since(UNIX_EPOCH).ok()?.as_secs() as i64,
        )
    }
}

pub fn routes() -> Router<Shared> {
    Router::new()
        .route(
            "/hooks/sentry",
            post(hooks::sentry).layer(DefaultBodyLimit::max(1 << 20)),
        )
        .route(
            "/hooks/github",
            post(hooks::github).layer(DefaultBodyLimit::max(1 << 20)),
        )
        .route(
            "/api/v1/deployment-gates/{id}",
            post(releases::gate).layer(DefaultBodyLimit::max(2048)),
        )
        .route(
            "/hooks/slack",
            post(chat::slack).layer(DefaultBodyLimit::max(16 << 10)),
        )
        .route(
            "/hooks/telegram",
            post(chat::telegram).layer(DefaultBodyLimit::max(16 << 10)),
        )
        .route(
            "/hooks/linear",
            post(linear::receive).layer(DefaultBodyLimit::max(1 << 20)),
        )
        .route(DISCOVERY, get(mcp::discovery))
        .route("/mcp", any(mcp::serve))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn introspection_binds_the_issuer_owner_expiry_and_resource() {
        let oauth = OAuth {
            issuer: "https://accounts.example".into(),
            subject: "owner".into(),
            client_id: "client".into(),
            client_secret: "secret".into(),
            introspection: "https://accounts.example/token".parse().unwrap(),
        };
        let audience = "https://work.example/mcp";
        let claims = json!({"active":true,"iss":oauth.issuer,"sub":"owner","scope":"workspace.read","exp":101,"aud":[audience]});
        let grant = |c| oauth.grant(serde_json::from_value(c).unwrap(), audience, 100);
        assert!(
            grant(claims.clone())
                .unwrap()
                .scopes
                .contains("workspace.read")
        );
        for (field, value) in [
            ("iss", json!("https://other.example")),
            ("sub", json!("other")),
            ("active", json!(false)),
            ("exp", json!(100)),
            ("aud", json!("https://other.example/mcp")),
        ] {
            let mut invalid = claims.clone();
            invalid[field] = value;
            assert!(grant(invalid).is_none());
        }
        let mut single = claims;
        single["aud"] = json!(audience);
        assert!(grant(single).is_some());
    }
}
