// SPDX-License-Identifier: MPL-2.0
use super::*;
use axum::{
    Extension,
    extract::{Path, Query, State},
    routing::{get, post},
};
use serde::Deserialize;
use std::collections::HashMap;

type Params = HashMap<String, String>;
#[derive(Deserialize, Default)]
struct Filters {
    environment: Option<String>,
}
async fn command(
    s: Shared,
    o: Owner,
    op: &'static str,
    p: Params,
    env: Option<String>,
    body: Value,
) -> WebResult<(StatusCode, Json<Value>)> {
    let input = json!({"workspace":o.workspace,"id":p.get("id").map(String::as_str).unwrap_or(""),
        "project":p.get("project").map(String::as_str).unwrap_or(""),"number":p.get("number").map(String::as_str).unwrap_or(""),
        "environment":env.unwrap_or_default(),"body":body});
    let mut result = s.domain(op, o.actor(), input).await?;
    let status = result["status"]
        .as_u64()
        .and_then(|n| u16::try_from(n).ok())
        .filter(|n| (200..300).contains(n))
        .and_then(|n| StatusCode::from_u16(n).ok())
        .ok_or_else(WebError::internal)?;
    Ok((
        status,
        Json(
            result
                .as_object_mut()
                .and_then(|r| r.remove("body"))
                .ok_or_else(WebError::internal)?,
        ),
    ))
}
// These queries resolve the cookie's unexpired session in the same database
// snapshot as the response. An HTTP caller cannot supply a workspace or actor.
pub(super) fn reads() -> Router<Shared> {
    let mut routes = Router::new();
    for (path, op) in [
        ("/api/v1/state", "workspace.state"),
        ("/api/v1/diagnostics", "workspace.diagnostics"),
    ] {
        routes = routes.route(
            path,
            get(
                move |State(s): State<Shared>, headers: axum::http::HeaderMap| async move {
                    let hash = session::session_hash(&headers)?;
                    s.coord_raw(op, service_actor(), Value::String(hash)).await
                },
            ),
        );
    }
    routes
}

pub(super) fn routes() -> Router<Shared> {
    let mut r = Router::new();
    r = r.route(
        "/api/v1/projects/{project}/attempts/{id}/draft-plan",
        get(|State(s), Extension(o), Path(p)| {
            command(s, o, "pull.draft_plan", p, None, Value::Null)
        }),
    );
    r = r.route(
        "/api/v1/projects/{project}/worker-starts",
        get(|State(s), Extension(o), Path(p)| command(s, o, "worker.starts", p, None, Value::Null)),
    );
    r = r.route(
        "/api/v1/projects/{project}/incidents/{id}/verification",
        get(
            |State(s), Extension(o), Path(p), Query(q): Query<Filters>| {
                command(s, o, "incident.verdict", p, q.environment, Value::Null)
            },
        ),
    );
    r = r.route(
        "/api/v1/projects/{project}/review-subjects",
        get(|State(s), Extension(o), Path(p)| {
            command(s, o, "policy_cases.subjects", p, None, Value::Null)
        }),
    );
    for (path, op) in [
        (
            "/api/v1/projects/{project}/incidents/{id}/related-work",
            "incident.related_work",
        ),
        (
            "/api/v1/projects/{project}/incidents/{id}/browser-qa",
            "incident.browser_qa",
        ),
        (
            "/api/v1/projects/{project}/pulls/{number}/browser-qa",
            "pull.browser_qa",
        ),
    ] {
        r = r.route(
            path,
            get(move |State(s), Extension(o), Path(p)| command(s, o, op, p, None, Value::Null)),
        );
    }
    r = r.route(
        "/api/v1/projects/{project}/policy-cases",
        get(|State(s), Extension(o), Path(p)| {
            command(s, o, "policy_cases.export", p, None, Value::Null)
        }),
    );
    for (path, op) in [
        (
            "/api/v1/projects/{project}/pulls/{number}/policy-case",
            "policy_cases.label",
        ),
        ("/api/v1/projects", "project.create"),
        (
            "/api/v1/projects/{project}/github-project/issues",
            "github_project.issues",
        ),
        (
            "/api/v1/projects/{project}/github-project/import",
            "github_project.import",
        ),
        (
            "/api/v1/projects/{project}/issues/{number}/start",
            "worker.start",
        ),
        ("/api/v1/releases", "release.create"),
        ("/api/v1/releases/{id}/cancel", "release.cancel"),
        ("/api/v1/members/{id}/chat", "member.chat"),
        (
            "/api/v1/projects/{project}/github",
            "project.connect_github",
        ),
        ("/api/v1/projects/{id}/policy", "project.policy"),
        (
            "/api/v1/projects/{project}/issues/{number}/linear",
            "linear.link",
        ),
        ("/api/v1/projects/{id}/retention", "project.retention"),
        ("/api/v1/limits", "workspace.limits"),
        ("/api/v1/budget", "workspace.budget"),
        ("/api/v1/devices/{id}/limits", "device.limits"),
        ("/api/v1/projects/{id}/dispatch", "project.dispatch"),
        ("/api/v1/decisions/{id}/answer", "decision.answer"),
        ("/api/v1/messages", "message.create"),
        ("/api/v1/plans", "plan.propose"),
        ("/api/v1/plans/{id}/approve", "plan.approve"),
        ("/api/v1/plans/{id}/evidence", "plan.evidence"),
        ("/api/v1/publications", "publication.propose"),
        ("/api/v1/publications/{id}/approve", "publication.approve"),
        (
            "/api/v1/projects/{project}/pulls/{number}/refresh",
            "pull.refresh",
        ),
        (
            "/api/v1/projects/{project}/pulls/{number}/evidence",
            "pull.evidence",
        ),
        (
            "/api/v1/projects/{project}/pulls/{number}/handover",
            "pull.handover",
        ),
        (
            "/api/v1/projects/{project}/pulls/{number}/watch",
            "pull.watch",
        ),
        (
            "/api/v1/projects/{project}/pulls/{number}/queue",
            "pull.queue",
        ),
        (
            "/api/v1/projects/{project}/pulls/{number}/approve",
            "pull.approve",
        ),
        (
            "/api/v1/projects/{project}/attempts/{id}/draft-pr",
            "pull.draft",
        ),
        (
            "/api/v1/projects/{project}/incidents/{id}/deployment-monitor",
            "deployment.configure",
        ),
        (
            "/api/v1/projects/{project}/incidents/{id}/deployment-holds/clear",
            "deployment.clear",
        ),
        (
            "/api/v1/projects/{project}/incidents/{id}/deployment-monitor/disable",
            "deployment.disable",
        ),
        ("/api/v1/usage", "usage.report"),
        ("/api/v1/usage/prices", "usage.price"),
        ("/api/v1/usage/threshold", "usage.threshold"),
        (
            "/api/v1/projects/{project}/usage/threshold",
            "usage.threshold",
        ),
        ("/api/v1/projects/{project}/usage", "usage.report"),
        ("/api/v1/incidents", "incident.report"),
        (
            "/api/v1/projects/{project}/incidents/{id}/link-issue",
            "incident.link_issue",
        ),
        (
            "/api/v1/projects/{project}/incidents/{id}/browser-qa",
            "incident.share_browser_qa",
        ),
        (
            "/api/v1/projects/{project}/incidents/{id}/promote",
            "incident.promote",
        ),
        (
            "/api/v1/projects/{project}/incidents/{id}/verification",
            "incident.receipt",
        ),
    ] {
        let handler = post(
            move |State(s), Extension(o), Path(p), ApiJson(b): ApiJson<Value>| {
                command(s, o, op, p, None, b)
            },
        );
        // Receipt submissions have a smaller public wire limit than other commands.
        r = r.route(
            path,
            match op {
                "incident.receipt" => handler.layer(axum::extract::DefaultBodyLimit::max(8192)),
                "incident.share_browser_qa" => {
                    handler.layer(axum::extract::DefaultBodyLimit::max(224 << 10))
                }
                _ => handler,
            },
        );
    }
    r
}
