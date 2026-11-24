// SPDX-License-Identifier: MPL-2.0
use super::super::{Shared, WebError, WebResult};
use crate::coord::{Db, Details, Error, Result};
use axum::{
    body::Bytes,
    extract::State,
    http::{HeaderMap, StatusCode},
    response::{IntoResponse, Response},
};
use hmac::{Hmac, Mac};
use regex::Regex;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::sync::LazyLock;
use time::{Duration, OffsetDateTime, format_description::well_known::Rfc3339};

fn fail(status: StatusCode, message: &str) -> WebError {
    WebError(status, message.into())
}
// Returning 503 asks the provider to redeliver. The delivery/event identity
// reconciles a commit whose response was interrupted.
fn intake_error(err: WebError) -> WebError {
    if err.0 == StatusCode::FORBIDDEN {
        err
    } else {
        fail(
            StatusCode::SERVICE_UNAVAILABLE,
            "Webhook intake temporarily unavailable",
        )
    }
}
fn header<'a>(h: &'a HeaderMap, key: &str) -> Option<&'a str> {
    h.get(key)?.to_str().ok()
}

fn authenticate(secret: &str, sig: Option<&str>, prefix: &str, body: &[u8]) -> WebResult<()> {
    if secret.is_empty() {
        return Err(fail(
            StatusCode::SERVICE_UNAVAILABLE,
            "Integration unavailable",
        ));
    }
    let mut mac =
        Hmac::<Sha256>::new_from_slice(secret.as_bytes()).expect("HMAC accepts any key length");
    mac.update(body);
    let signature = sig
        .and_then(|s| s.strip_prefix(prefix))
        .and_then(|s| hex::decode(s).ok());
    if !signature.is_some_and(|bytes| mac.verify_slice(&bytes).is_ok()) {
        return Err(fail(StatusCode::UNAUTHORIZED, "Invalid signature"));
    }
    Ok(())
}

static UUID: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$").unwrap()
});
static LABEL: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[A-Za-z0-9][A-Za-z0-9._+-]{0,79}$").unwrap());
static CLASS: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[A-Za-z][A-Za-z0-9_.]{0,60}(Error|Exception|Panic)$").unwrap());

fn label(s: &str) -> &str {
    if LABEL.is_match(s) && crate::redact::shareable(s) {
        s
    } else {
        ""
    }
}
fn numeric_id(v: &Value) -> Option<i64> {
    v.as_i64()
        .or_else(|| v.as_str()?.parse().ok())
        .filter(|x| *x > 0)
}
fn text<'a>(v: &'a Value, k: &str) -> &'a str {
    v[k].as_str().unwrap_or_default()
}
fn entries(v: &Value) -> impl Iterator<Item = &Value> {
    v.as_array().into_iter().flatten()
}

#[derive(Serialize, Deserialize)]
struct Observation {
    installation: String,
    project: i64,
    event: String,
    group: String,
    seen: String,
    observed: String,
    environment: String,
    release: String,
    frames: Vec<Frame>,
}
#[derive(Serialize, Deserialize)]
struct Frame {
    file: String,
    line: i64,
}
fn normalize(body: &[u8]) -> Option<Observation> {
    let p: Value = serde_json::from_slice(body).ok()?;
    let installation = p["installation"]["uuid"].as_str()?;
    if p["action"] != "created" || !UUID.is_match(installation) {
        return None;
    }
    let e = &p["data"]["error"];
    let project = numeric_id(&e["project"])?;
    let group = numeric_id(&e["issue_id"])?;
    let event = e["event_id"].as_str()?;
    let seen = e["datetime"].as_str()?;
    let time = OffsetDateTime::parse(seen, &Rfc3339).ok()?;
    if event.len() != 32
        || !event.bytes().all(|c| c.is_ascii_hexdigit())
        || time.year() <= 1
        || time > OffsetDateTime::now_utc() + Duration::minutes(5)
    {
        return None;
    }
    let installation = installation.to_ascii_lowercase();
    let prefix = format!("{installation}/{project}");
    let mut environment = text(e, "environment");
    let mut release = text(e, "release");
    for tag in entries(&e["tags"]) {
        if tag.as_array().is_none_or(|pair| pair.len() != 2) {
            continue;
        }
        if let Some(s) = tag[1].as_str() {
            if tag[0] == "release" && release.is_empty() {
                release = s
            }
            if tag[0] == "environment" && environment.is_empty() {
                environment = s
            }
        }
    }
    let commit = matches!(release.len(), 40 | 64)
        && release
            .bytes()
            .all(|c| c.is_ascii_digit() || matches!(c, b'a'..=b'f'));
    let mut o = Observation {
        installation,
        project,
        event: format!("{prefix}/{}", event.to_ascii_lowercase()),
        group: format!("{prefix}/{group}"),
        seen: seen.into(),
        observed: "Application error reported by Sentry".into(),
        environment: label(environment).into(),
        release: if commit { release } else { label(release) }.into(),
        frames: vec![],
    };
    for exception in entries(&e["exception"]["values"]) {
        let class = text(exception, "type");
        if CLASS.is_match(class) && crate::redact::shareable(class) {
            o.observed = format!("{class} reported by Sentry")
        }
        for frame in entries(&exception["stacktrace"]["frames"]) {
            if o.frames.len() == 8 {
                break;
            }
            let line = frame["lineno"].as_i64().unwrap_or(0);
            if frame["in_app"] != true || line <= 0 {
                continue;
            }
            let path = text(frame, "filename")
                .split(['?', '#'])
                .next()
                .unwrap_or("");
            let name = label(path.rsplit(['/', '\\']).next().unwrap_or(""));
            if !name.is_empty() {
                o.frames.push(Frame {
                    file: name.into(),
                    line,
                })
            }
        }
    }
    Some(o)
}

pub(super) async fn sentry(
    State(state): State<Shared>,
    headers: HeaderMap,
    body: Bytes,
) -> WebResult<Response> {
    authenticate(
        &state.integrations.sentry_secret,
        header(&headers, "sentry-hook-signature"),
        "",
        &body,
    )?;
    if header(&headers, "sentry-hook-resource") != Some("error") {
        return Err(fail(
            StatusCode::UNPROCESSABLE_ENTITY,
            "Subscribe to error.created",
        ));
    }
    let o = normalize(&body)
        .ok_or_else(|| fail(StatusCode::UNPROCESSABLE_ENTITY, "Unsupported error event"))?;
    let result = state
        .coord(
            "hooks.sentry",
            ["provider".into(), format!("sentry/{}", o.installation)],
            json!(o),
        )
        .await
        .map_err(intake_error)?;
    if result["limited"] == true {
        return Ok((
            StatusCode::TOO_MANY_REQUESTS,
            [("Retry-After", "60")],
            "Project intake limit",
        )
            .into_response());
    }
    Ok(StatusCode::ACCEPTED.into_response())
}

#[derive(Serialize, Deserialize)]
struct GithubSignal {
    installation: i64,
    repository: i64,
    issue: i64,
    issue_event: bool,
    removed: Option<Vec<i64>>,
    substantive: bool,
    delivery: String,
    digest: String,
}
fn github_signal(delivery: &str, event: &str, body: &[u8]) -> Option<GithubSignal> {
    if !UUID.is_match(delivery) {
        return None;
    }
    let p: Value = serde_json::from_slice(body).ok()?;
    let installation = p["installation"]["id"].as_i64().filter(|n| *n > 0)?;
    let repository = p["repository"]["id"].as_i64().unwrap_or(0);
    let issue = p["issue"]["id"].as_i64().unwrap_or(0);
    let number = p["issue"]["number"].as_i64().unwrap_or(0);
    let action = p["action"].as_str()?;
    let issue_event = event == "issues" && repository > 0 && issue > 0 && number > 0;
    let removed = if event == "installation_repositories" && action == "removed" {
        let ids = p["repositories_removed"]
            .as_array()?
            .iter()
            .map(|r| r["id"].as_i64().filter(|v| *v > 0))
            .collect::<Option<Vec<i64>>>()?;
        if ids.is_empty() {
            return None;
        }
        Some(ids)
    } else {
        None
    };
    let revoke_all = event == "installation"
        && matches!(action, "suspend" | "deleted")
        && repository == 0
        && issue == 0;
    if !issue_event && !revoke_all && removed.is_none() {
        return None;
    }
    let substantive = matches!(action, "closed" | "reopened" | "deleted" | "transferred")
        || action == "edited"
            && p["changes"]
                .as_object()
                .is_some_and(|m| m.contains_key("title") || m.contains_key("body"));
    Some(GithubSignal {
        installation,
        repository,
        issue,
        issue_event,
        removed,
        substantive,
        delivery: delivery.into(),
        digest: hex::encode(Sha256::digest(body)),
    })
}
pub(super) async fn github(
    State(state): State<Shared>,
    headers: HeaderMap,
    body: Bytes,
) -> WebResult<StatusCode> {
    authenticate(
        &state.integrations.github_secret,
        header(&headers, "x-hub-signature-256"),
        "sha256=",
        &body,
    )?;
    if header(&headers, "x-github-event") == Some("merge_group") {
        let delivery = header(&headers, "x-github-delivery").unwrap_or("");
        let v: Value = serde_json::from_slice(&body)
            .map_err(|_| fail(StatusCode::UNPROCESSABLE_ENTITY, "Invalid merge group"))?;
        if v["action"] != "checks_requested" {
            return Ok(StatusCode::ACCEPTED);
        }
        let group = queue_group(delivery, &v)
            .ok_or_else(|| fail(StatusCode::UNPROCESSABLE_ENTITY, "Invalid merge group"))?;
        state
            .coord(
                "hooks.merge_group",
                [
                    "provider".into(),
                    format!("github/{}", group["installation"]),
                ],
                group,
            )
            .await
            .map_err(intake_error)?;
        return Ok(StatusCode::ACCEPTED);
    }
    let p = github_signal(
        header(&headers, "x-github-delivery").unwrap_or(""),
        header(&headers, "x-github-event").unwrap_or(""),
        &body,
    )
    .ok_or_else(|| {
        fail(
            StatusCode::UNPROCESSABLE_ENTITY,
            "Unsupported installation delivery",
        )
    })?;
    state
        .coord(
            "hooks.github",
            ["provider".into(), format!("github/{}", p.installation)],
            json!(p),
        )
        .await
        .map_err(intake_error)?;
    Ok(StatusCode::ACCEPTED)
}

fn queue_group(delivery: &str, v: &Value) -> Option<Value> {
    let g = &v["merge_group"];
    let head = g["head_sha"].as_str()?;
    let base = g["base_sha"].as_str()?;
    let target = g["base_ref"].as_str()?.strip_prefix("refs/heads/")?;
    let head_ref = g["head_ref"].as_str()?.strip_prefix("refs/heads/")?;
    let oid = |s: &str| {
        [40, 64].contains(&s.len())
            && s.bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    };
    let branch = |s: &str| {
        !s.is_empty()
            && s.len() <= 240
            && !s.contains("..")
            && s.bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"._/-".contains(&b))
    };
    if !UUID.is_match(delivery)
        || !oid(head)
        || !oid(base)
        || head == base
        || !branch(target)
        || !branch(head_ref)
        || !head_ref.starts_with("gh-readonly-queue/")
    {
        return None;
    }
    Some(
        json!({"installation":numeric_id(&v["installation"]["id"])?,"repository":numeric_id(&v["repository"]["id"])?,
        "head":head,"base":base,"target":target,"headRef":head_ref,"delivery":delivery}),
    )
}

fn record_merge_group(db: &Db<'_>, g: Value) -> Result<Value> {
    let id = |name: &str| {
        g[name]
            .as_i64()
            .map(|x| x.to_string())
            .ok_or_else(|| Error::bad("Invalid merge group"))
    };
    let (installation, repository) = (id("installation")?, id("repository")?);
    let mapping = db.optional("SELECT workspace_id,project_id FROM github_mappings WHERE installation_id=$1 AND repository_id=$2 AND active FOR SHARE",&[&installation,&repository])?.ok_or_else(Error::denied)?;
    let (w, p) = (mapping.text(0)?, mapping.text(1)?);
    db.exec("INSERT INTO github_merge_groups(workspace_id,project_id,installation_id,repository_id,delivery,head,base,head_ref,target)
      SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9 WHERE EXISTS (SELECT FROM pull_queue_permits WHERE workspace_id=$1 AND project_id=$2 AND target=$9 AND base=$7 AND state IN ('requested','submitting','awaiting_queue','queued','checking') AND expires_at>now())
      ON CONFLICT DO NOTHING", &[w,p,&installation,&repository,text(&g,"delivery"),text(&g,"head"),text(&g,"base"),text(&g,"headRef"),text(&g,"target")])?;
    Ok(Value::Null)
}

pub(crate) fn apply(db: &Db<'_>, op: &str, input: Value) -> Result<Value> {
    match op {
        "hooks.merge_group" => record_merge_group(db, input),
        "hooks.sentry" => record_observation(db, serde_json::from_value(input)?),
        "hooks.github" => record_github(db, serde_json::from_value(input)?),
        _ => Err(Error::bad("Unknown webhook operation")),
    }
}

fn record_observation(db: &Db<'_>, o: Observation) -> Result<Value> {
    let mapping=db.optional("SELECT m.workspace_id,m.project_id FROM sentry_mappings m JOIN projects p ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id) WHERE m.installation_uuid=$1 AND m.provider_project=$2 AND m.active FOR NO KEY UPDATE OF p FOR SHARE OF m",&[&o.installation,&o.project.to_string()])?.ok_or_else(Error::denied)?;
    let (ws, project) = (mapping.text(0)?, mapping.text(1)?);
    let limited=db.one("SELECT NOT EXISTS(SELECT FROM incident_inbox WHERE workspace_id=$1 AND project_id=$2 AND source='sentry' AND event_key=$3) AND (SELECT count(*) FROM (SELECT 1 FROM incident_inbox WHERE workspace_id=$1 AND project_id=$2 AND source='sentry' AND accepted_at>now()-interval '1 minute' LIMIT 600) recent)>=600",&[ws,project,&o.event])?.bool(0)?;
    if limited {
        return Ok(json!({"limited":true}));
    }
    if let Some(inbox)=db.optional("INSERT INTO incident_inbox(workspace_id,project_id,source,event_key,group_key,observed,expected,steps,release,environment,frames,seen_at) VALUES($1,$2,'sentry',$3,$4,$5,'','',$6,$7,$8::jsonb,$9::timestamptz) ON CONFLICT(workspace_id,project_id,source,event_key) DO NOTHING RETURNING id",
        &[ws,project,&o.event,&o.group,&o.observed,&o.release,&o.environment,&serde_json::to_string(&o.frames)?,&o.seen])?{
        db.event(ws,project,"sentry.event_accepted",inbox.text(0)?,("provider",&format!("sentry/{}",o.installation)),Details{reason:"Signed Sentry event matched the authorized project",evidence:&o.event,..Default::default()})?;
    }
    Ok(Value::Null)
}

fn record_github(db: &Db<'_>, p: GithubSignal) -> Result<Value> {
    let bindings=db.rows("SELECT workspace_id,project_id FROM github_mappings WHERE installation_id=$1 AND ($2::bigint=0 OR repository_id=$2) AND (active OR NOT $3::boolean) AND ($4::jsonb='null'::jsonb OR repository_id IN(SELECT value::bigint FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof($4::jsonb)='array' THEN $4::jsonb ELSE '[]'::jsonb END))) ORDER BY workspace_id,project_id,repository_id FOR UPDATE",
       &[&p.installation.to_string(),&p.repository.to_string(),&p.issue_event.to_string(),&serde_json::to_string(&p.removed)?])?;
    if bindings.is_empty() {
        return Err(Error::denied());
    }
    for row in bindings {
        let (ws, project) = (row.text(0)?, row.text(1)?);
        if db.exec("INSERT INTO github_deliveries(workspace_id,project_id,delivery) VALUES($1,$2,$3) ON CONFLICT DO NOTHING",&[ws,project,&p.delivery])?==0 {continue}
        let (kind, subject) = if p.issue_event {
            let issue = p.issue.to_string();
            db.exec("UPDATE incident_issues SET next_run=now() WHERE workspace_id=$1 AND project_id=$2 AND issue_id=$3 AND status='linked'",&[ws,project,&issue])?;
            db.exec("WITH fresh AS (INSERT INTO planned_issue_signals(workspace_id,project_id,github_id,signal_hash)
                SELECT workspace_id,project_id,github_id,$4 FROM planned_issue_state WHERE workspace_id=$1 AND project_id=$2 AND github_id=$3 ON CONFLICT DO NOTHING RETURNING github_id)
                UPDATE planned_issue_state SET availability='pending',reason='Awaiting authoritative GitHub read',next_check=now(),
                generation=generation+CASE WHEN $5::boolean THEN 1 ELSE 0 END WHERE workspace_id=$1 AND project_id=$2 AND github_id IN(SELECT github_id FROM fresh)",&[ws,project,&issue,&p.digest,&p.substantive.to_string()])?;
            ("github.issue_change_received", issue)
        } else {
            db.exec(
                "UPDATE github_mappings SET active=false WHERE workspace_id=$1 AND project_id=$2",
                &[ws, project],
            )?;
            db.exec("UPDATE planned_issue_state SET availability='unknown',reason='Repository mapping disabled',generation=generation+1,next_check=now() WHERE workspace_id=$1 AND project_id=$2 AND reason<>'Repository mapping disabled'",&[ws,project])?;
            ("github.mapping_disabled", project.into())
        };
        db.event(
            ws,
            project,
            kind,
            &subject,
            ("provider", &format!("github/{}", p.installation)),
            Details {
                reason: "Signed GitHub delivery matched the installation mapping",
                evidence: &p.delivery,
                ..Default::default()
            },
        )?;
    }
    Ok(Value::Null)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn github_published_signature_vector_matches_only_the_original_bytes() {
        let signature =
            Some("sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17");
        let secret = "It's a Secret to Everybody";
        assert!(authenticate(secret, signature, "sha256=", b"Hello, World!").is_ok());
        assert_eq!(
            authenticate(secret, signature, "sha256=", b"Hello, World! ")
                .unwrap_err()
                .0,
            StatusCode::UNAUTHORIZED
        );
    }

    #[test]
    fn signed_group_subject_requires_real_refs_and_distinct_commits() {
        let delivery = "00112233-aabb-ccdd-eeff-001122334455";
        let mut e = json!({"installation":{"id":10},"repository":{"id":11},"merge_group":{
            "head_sha":"a".repeat(40),"base_sha":"b".repeat(40),"base_ref":"refs/heads/main","head_ref":"refs/heads/gh-readonly-queue/main/pr-12-example"}});
        let group = queue_group(delivery, &e).unwrap();
        assert_eq!(group["target"], "main");
        e["merge_group"]["base_sha"] = e["merge_group"]["head_sha"].clone();
        assert!(queue_group(delivery, &e).is_none());
        e["merge_group"]["base_sha"] = json!("b".repeat(40));
        e["merge_group"]["head_ref"] = json!("refs/heads/bugsnitch/user");
        assert!(queue_group(delivery, &e).is_none());
    }
    #[test]
    fn sentry_keeps_only_authorized_fields_and_uses_tag_fallbacks() {
        let event = json!({"action":"created","installation":{"uuid":"00112233-aabb-ccdd-eeff-001122334455"},
            "data":{"error":{"project":"71","issue_id":1,"event_id":"a".repeat(32),"datetime":"2026-01-01T00:00:00Z",
            "request":{"cookies":"PRIVATE_EVIDENCE_MARKER"},"tags":[["release","v2"],["environment","staging"],["private","PRIVATE_EVIDENCE_MARKER"]],
            "exception":{"values":[{"type":"TypeError","value":"PRIVATE_EVIDENCE_MARKER","stacktrace":{"frames":[
                {"in_app":true,"lineno":11,"filename":"C:\\src\\app.js?token=PRIVATE_EVIDENCE_MARKER","vars":"PRIVATE_EVIDENCE_MARKER"},
                {"in_app":false,"lineno":1,"filename":"ignored.js"}]}}]}}}});
        let observation = normalize(&serde_json::to_vec(&event).unwrap()).unwrap();
        assert_eq!(observation.environment, "staging");
        assert_eq!(observation.release, "v2");
        assert_eq!(observation.observed, "TypeError reported by Sentry");
        assert_eq!(observation.frames.len(), 1);
        assert_eq!(observation.frames[0].file, "app.js");
        assert!(
            !serde_json::to_string(&observation)
                .unwrap()
                .contains("PRIVATE_EVIDENCE_MARKER")
        );
    }
}
