// SPDX-License-Identifier: MPL-2.0
use crate::coord::{Db, Error, Result};
use serde::Deserialize;
use serde_json::Value;

const PROJECTS: &str = include_str!("../../../internal/transport/views/projects.sql");
const ATTEMPTS: &str = include_str!("../../../internal/transport/views/attempts.sql");
const DECISIONS: &str = include_str!("../../../internal/transport/views/decisions.sql");

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Empty {}
// The public MCP endpoint authenticates the owner grant and checks its read
// scope. The workspace is fixed by this single-owner grant, never tool input.
fn authorize(db: &Db<'_>) -> Result<()> {
    if matches!(db.actor(), ("plugin", "oauth-owner-grant" | "local-grant")) {
        Ok(())
    } else {
        Err(Error::denied())
    }
}

pub fn is_list(op: &str) -> bool {
    matches!(
        op,
        "plugin.list_projects" | "plugin.list_attempts" | "plugin.list_decisions"
    )
}

pub fn list(db: &Db<'_>, op: &str, input: Value) -> Result<String> {
    authorize(db)?;
    serde_json::from_value::<Empty>(input)?;
    let view = match op {
        "plugin.list_projects" => PROJECTS,
        "plugin.list_attempts" => ATTEMPTS,
        "plugin.list_decisions" => DECISIONS,
        _ => return Err(Error::bad("Unknown plugin list")),
    };
    let query =
        format!("SELECT COALESCE(json_agg(t),'[]') FROM (SELECT * FROM ({view}) v LIMIT 100) t");
    db.one(&query, &["personal"])?.into_text()
}
