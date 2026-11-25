// SPDX-License-Identifier: MPL-2.0
use super::{capacity, plan};
use crate::coord::{Db, Error, Result};
use serde::Deserialize;
use serde_json::{Value, json};
use std::collections::{HashMap, HashSet};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Next {
    #[serde(rename = "projectId")]
    project: String,
    #[serde(default, rename = "waitForTurn")]
    wait_for_turn: bool,
    #[serde(default)]
    issues: Vec<i64>,
}

// This uses a repeatable-read transaction. Selection explains what could be
// admitted. Only a subsequent claim can reserve capacity or issue ownership.
pub(super) fn next(db: &Db<'_>, token: &str, request: Value) -> Result<Value> {
    let Next {
        project,
        wait_for_turn,
        issues,
    } = serde_json::from_value(request)?;
    let requested: HashSet<_> = issues.iter().copied().collect();
    if requested.len() != issues.len() || issues.len() > 32 || issues.iter().any(|n| *n < 1) {
        return Err(Error::bad(
            "Choose one to 32 distinct positive issue numbers",
        ));
    }
    let row = db.optional("SELECT w.id,d.id,p.repository,p.policy_version,p.paused,w.stopped,w.ticket_limit,w.turn_limit,d.turn_limit,
        EXISTS(SELECT FROM github_mappings g WHERE g.workspace_id=w.id AND g.project_id=p.id AND g.active),
        (SELECT count(*) FROM admission a WHERE a.workspace_id=w.id AND a.active)
        FROM device_projects m JOIN devices d ON (m.workspace_id,m.device_id)=(d.workspace_id,d.id) JOIN workspaces w ON w.id=d.workspace_id
        JOIN projects p ON (m.workspace_id,m.project_id)=(p.workspace_id,p.id)
        WHERE p.id=$1 AND NOT d.revoked AND d.token_hash=$2", &[&project,token])?.ok_or_else(Error::denied)?;
    let ws = row.text(0)?;
    let device = row.text(1)?;
    let repository = row.text(2)?;
    let hold = row.bool(5)? || row.bool(4)? || !row.bool(9)?;
    let reason = if row.bool(5)? {
        "Workspace dispatch is stopped"
    } else if row.bool(4)? {
        "Project dispatch is paused"
    } else if !row.bool(9)? {
        "The project needs an active GitHub App repository mapping"
    } else if row.i64(10)? >= row.i64(6)? {
        "All workspace implementation slots are reserved"
    } else if !wait_for_turn && !capacity(db, ws, device, row.i64(7)?, row.i64(8)?)? {
        "All workspace or device model-turn slots are reserved"
    } else {
        ""
    };
    if !reason.is_empty() && (issues.is_empty() || hold) {
        if !issues.is_empty() {
            return Ok(json!({"reason":reason,"hold":true}));
        }
        return Ok(json!({"reason":reason}));
    }
    let Some(mut plan) = plan::approved(db, ws, &project)? else {
        let reason = "Approve a plan before requesting its next issue";
        return Ok(if issues.is_empty() {
            json!({"reason":reason})
        } else {
            json!({"reason":reason,"hold":true})
        });
    };
    let facts = ticket_facts(db, ws, &project, &plan.id)?;
    plan.tickets.sort_by_key(|t| {
        (
            t.priority,
            !facts.get(&t.number).is_some_and(|f| f.delegated),
            t.number,
        )
    });
    let mut candidates = Vec::with_capacity(plan.tickets.len());
    for ticket in &plan.tickets {
        if issues.is_empty() || requested.contains(&ticket.number) {
            candidates.push(candidate(&facts, repository, ticket)?);
        }
    }
    let mut snapshot = json!({"plan":plan.document,"scope":{"deviceId":device,"projectId":project,"planId":plan.id,"policyVersion":row.i64(3)?},"candidates":candidates});
    if !issues.is_empty() {
        let bindings: HashMap<_, _> = issues.into_iter().filter_map(|number| {
            let issue = facts.get(&number)?.canonical.as_ref()?;
            // Freshness is checked at admission. A temporary refresh outage
            // must not rewrite the last observed identity or revision.
                if !issue.bound || issue.state != "open" || issue.revision.is_empty() || issue.digest.len() != 64 {
                return None;
            }
            Some((number, json!({"githubId":issue.id,"requirementsDigest":issue.digest,"updatedAt":issue.revision})))
        }).collect();
        snapshot["bindings"] = json!(bindings);
        if !reason.is_empty() {
            snapshot["wait"] = json!(reason);
        }
    }
    Ok(snapshot)
}

struct Canonical {
    bound: bool,
    fresh_and_bound: bool,
    state: String,
    id: i64,
    digest: String,
    title: String,
    revision: String,
}

impl Canonical {
    fn eligible(&self, allow_closed: bool) -> bool {
        self.fresh_and_bound && (self.state == "open" || allow_closed && self.state == "closed")
    }
}

struct TicketFacts {
    delegated: bool,
    attempted: bool,
    integrated: bool,
    reserved: bool,
    canonical: Option<Canonical>,
}

// Read one row per planned issue. The plan validator requires dependencies
// to refer to issues in this same set. Evaluating every edge below is local,
// so a full 64-ticket plan takes the same four SQL calls as a single ticket.
fn ticket_facts(
    db: &Db<'_>,
    ws: &str,
    project: &str,
    plan_id: &str,
) -> Result<HashMap<i64, TicketFacts>> {
    let rows = db.rows(
        "WITH tickets AS MATERIALIZED (
            SELECT t.number,t.resources FROM plan_versions v
            CROSS JOIN LATERAL jsonb_to_recordset(v.document->'tickets') AS t(number bigint,resources text[])
            WHERE v.workspace_id=$1 AND v.project_id=$2 AND v.id=$3
        ), attempted AS (
            SELECT DISTINCT a.issue_number FROM admission a JOIN tickets t ON t.number=a.issue_number
            WHERE a.workspace_id=$1 AND a.project_id=$2
        ), reservations AS MATERIALIZED (
            SELECT resources FROM admission WHERE workspace_id=$1 AND owns_issue AND attempt_id<>'' AND resources<>'[]'::jsonb
        )
        SELECT t.number, a.issue_number IS NOT NULL, e.issue_number IS NOT NULL,
            EXISTS(SELECT FROM reservations r WHERE r.resources ?| t.resources),
            z.source_state, coalesce(m.active AND z.availability='current' AND z.checked_at>now()-interval '20 minutes'
                AND b.plan_id IS NOT NULL,false), z.github_id, z.digest, z.title, z.selection_revision, EXISTS(SELECT FROM linear_sessions s WHERE s.workspace_id=$1 AND s.project_id=$2 AND s.issue_number=t.number AND NOT s.finished), b.plan_id IS NOT NULL
        FROM tickets t
        LEFT JOIN attempted a ON a.issue_number=t.number
        LEFT JOIN prerequisite_evidence e ON e.workspace_id=$1 AND e.project_id=$2 AND e.plan_id=$3 AND e.issue_number=t.number
        LEFT JOIN planned_issue_state z ON z.workspace_id=$1 AND z.project_id=$2 AND z.number=t.number
        LEFT JOIN github_mappings m ON (m.workspace_id,m.project_id,m.installation_id,m.repository_id)=
            (z.workspace_id,z.project_id,z.installation_id,z.repository_id)
        LEFT JOIN plan_issue_bindings b ON b.plan_id=$3 AND
            (b.workspace_id,b.project_id,b.number,b.generation)=(z.workspace_id,z.project_id,z.number,z.generation)",
        &[ws, project, plan_id],
    )?;
    let mut facts = HashMap::with_capacity(rows.len());
    for row in rows {
        let canonical = if let Some(state) = row.opt(4)? {
            Some(Canonical {
                bound: row.bool(11)?,
                state: state.into(),
                fresh_and_bound: row.bool(5)?,
                id: row.i64(6)?,
                digest: row.text(7)?.into(),
                title: row.text(8)?.into(),
                revision: row.text(9)?.into(),
            })
        } else {
            None
        };
        facts.insert(
            row.i64(0)?,
            TicketFacts {
                delegated: row.bool(10)?,
                attempted: row.bool(1)?,
                integrated: row.bool(2)?,
                reserved: row.bool(3)?,
                canonical,
            },
        );
    }
    Ok(facts)
}

fn candidate(
    facts: &HashMap<i64, TicketFacts>,
    repository: &str,
    ticket: &plan::Ticket,
) -> Result<Value> {
    let n = ticket.number;
    let blocked = |reason: &str| json!({"number":n,"reason":reason});
    let state = facts.get(&n).ok_or_else(Error::internal)?;
    if state.attempted {
        return Ok(blocked(
            "an attempt already exists; inspect or explicitly retry it",
        ));
    }
    if state.integrated {
        return Ok(blocked("the plan already records integration evidence"));
    }
    if state
        .canonical
        .as_ref()
        .is_some_and(|issue| !issue.eligible(false))
    {
        return Ok(blocked(plan::DRIFT));
    }
    let mut unbound_dep = false;
    for dep in ticket.dependencies() {
        let prerequisite = facts.get(dep).ok_or_else(Error::internal)?;
        if prerequisite
            .canonical
            .as_ref()
            .is_some_and(|issue| !issue.eligible(true))
        {
            return Ok(blocked(plan::DRIFT));
        }
        if !prerequisite.integrated {
            return Ok(blocked("Waiting for prerequisite integration evidence"));
        }
        unbound_dep |= prerequisite.canonical.is_none();
    }
    if state.reserved {
        return Ok(blocked("A shared resource is reserved"));
    }
    if unbound_dep {
        return Ok(blocked(
            "a prerequisite lacks a verified canonical issue revision in this plan",
        ));
    }
    let Some(issue) = &state.canonical else {
        return Ok(blocked(
            "the approved plan lacks a verified canonical GitHub issue revision",
        ));
    };
    if issue.revision.is_empty() || issue.digest.len() != 64 {
        return Ok(blocked(
            "awaiting a canonical GitHub timestamp and a title suitable for sharing",
        ));
    }
    Ok(json!({"number":n, "selection":{
        "githubId":issue.id, "requirementsDigest":issue.digest, "schema":1, "repository":repository,
        "number":n, "url":format!("https://github.com/{repository}/issues/{n}"),
        "title":issue.title, "state":"open", "updatedAt":issue.revision
    }}))
}
