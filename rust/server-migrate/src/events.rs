// SPDX-License-Identifier: MPL-2.0
use crate::coord::{Db, Error, Result};
use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    workspace: String,
    #[serde(default)]
    after: i64,
    #[serde(default)]
    limit: usize,
}

// Retain seq as the historical event identity. The commit-ordered position is
// sent as a string, so JavaScript consumers can round-trip all bigint cursors.
pub(crate) const FIELDS: &str = r#"seq,position::text AS cursor,project_id AS "projectId",kind,subject_id AS "subjectId",created_at AS "createdAt",actor_source AS "actorSource",actor_id AS "actorId",reason,policy_version AS "policyVersion",subject_version AS "subjectVersion",evidence_ref AS "evidenceRef""#;

pub fn handle(db: &Db<'_>, op: &str, input: Value) -> Result<Value> {
    let r: Request = serde_json::from_value(input)?;
    let (sql, args) = match op {
        "events.snapshot" => (
            format!(
                "SELECT row_to_json(e) FROM (SELECT {FIELDS} FROM events WHERE workspace_id=$1 ORDER BY position DESC LIMIT 150)e"
            ),
            vec![r.workspace],
        ),
        "events.replay" if r.after >= 0 && (1..=150).contains(&r.limit) => (
            format!(
                "SELECT row_to_json(e) FROM (SELECT {FIELDS} FROM events WHERE workspace_id=$1 AND position>$2::bigint ORDER BY position LIMIT $3::int)e"
            ),
            vec![r.workspace, r.after.to_string(), (r.limit + 1).to_string()],
        ),
        _ => return Err(Error::bad("Invalid event page")),
    };
    let params = args.iter().map(String::as_str).collect::<Vec<_>>();
    let mut events = db
        .rows(&sql, &params)?
        .into_iter()
        .map(|row| serde_json::from_str::<Value>(row.text(0)?).map_err(|_| Error::internal()))
        .collect::<Result<Vec<_>>>()?;
    if op == "events.snapshot" {
        return Ok(Value::Array(events));
    }
    let more = events.len() > r.limit;
    events.truncate(r.limit);
    let next = events
        .last()
        .and_then(|v| v["cursor"].as_str())
        .map(str::to_owned)
        .unwrap_or_else(|| r.after.to_string());
    Ok(json!({"events":events,"nextCursor":next,"hasMore":more}))
}
