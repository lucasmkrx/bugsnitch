// SPDX-License-Identifier: MPL-2.0
use super::{WebResult, invalid};
use crate::redact::shareable;
use serde::Deserialize;
use serde_json::{Value, json};
use std::collections::{HashMap, HashSet};
use time::{Date, format_description::well_known::Iso8601};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Ticket {
    number: i64,
    title: String,
    criteria: Vec<String>,
    verification: String,
    estimate_hours: i64,
    priority: i64,
    dependencies: Option<Vec<i64>>,
    resources: Option<Vec<String>>,
}
#[derive(Deserialize)]
struct Plan {
    title: String,
    milestone: String,
    #[serde(default)]
    assumptions: String,
    #[serde(default)]
    target: String,
    #[serde(default)]
    forecast: String,
    tickets: Vec<Ticket>,
}
fn label(s: &str, max: usize) -> bool {
    !s.trim().is_empty() && s.len() <= max
}
fn valid_day(s: &str) -> bool {
    s.is_empty() || (s.len() == 10 && Date::parse(s, &Iso8601::DATE).is_ok())
}

impl Plan {
    // Reject a corrupt stored plan on read as the original public API did. A
    // recommendation is assembled solely from this repeatable-read snapshot.
    fn validate(&self) -> WebResult<()> {
        if !label(&self.title, 160)
            || !label(&self.milestone, 240)
            || self.assumptions.len() > 2000
            || !valid_day(&self.target)
            || !valid_day(&self.forecast)
            || !(1..=64).contains(&self.tickets.len())
        {
            return Err(invalid());
        }
        let mut numbers = HashSet::new();
        for ticket in &self.tickets {
            let resources = ticket.resources.as_deref().unwrap_or_default();
            if ticket.number <= 0
                || !numbers.insert(ticket.number)
                || !label(&ticket.title, 200)
                || !label(&ticket.verification, 1000)
                || !(1..=16).contains(&ticket.criteria.len())
                || !ticket.criteria.iter().all(|s| label(s, 1000))
                || !(1..=16).contains(&ticket.estimate_hours)
                || !(0..=4).contains(&ticket.priority)
                || ticket.dependencies.as_deref().unwrap_or_default().len() > 64
                || resources.len() > 16
                || !resources.iter().all(|s| {
                    s.len() <= 64
                        && s.bytes().enumerate().all(|(i, b)| {
                            b.is_ascii_lowercase()
                                || b.is_ascii_digit()
                                || (i > 0 && b"._-".contains(&b))
                        })
                        && !s.is_empty()
                })
            {
                return Err(invalid());
            }
        }
        let mut done = HashSet::new();
        while done.len() < numbers.len() {
            let prior = done.len();
            for t in &self.tickets {
                if t.dependencies
                    .as_deref()
                    .unwrap_or_default()
                    .iter()
                    .all(|n| done.contains(n))
                {
                    done.insert(t.number);
                }
            }
            if prior == done.len() {
                return Err(invalid());
            }
        }
        Ok(())
    }
}

#[derive(Deserialize)]
struct Candidate {
    number: i64,
    reason: Option<String>,
    selection: Option<Value>,
}
#[derive(Deserialize)]
struct Snapshot {
    reason: Option<String>,
    hold: Option<bool>,
    wait: Option<String>,
    bindings: Option<Value>,
    plan: Option<Plan>,
    scope: Option<Value>,
    candidates: Option<Vec<Candidate>>,
}

pub(super) fn project(input: Value) -> WebResult<Value> {
    let snapshot: Snapshot = serde_json::from_value(input).map_err(|_| invalid())?;
    if let Some(reason) = snapshot.reason {
        return Ok(if snapshot.hold == Some(true) {
            json!({"reason":reason,"hold":true})
        } else {
            json!({"reason":reason})
        });
    }
    let plan = snapshot.plan.ok_or_else(invalid)?;
    plan.validate()?;
    let scope = snapshot.scope.ok_or_else(invalid)?;
    let mut output = match snapshot.bindings {
        Some(bindings) => json!({"scope":scope,"bindings":bindings}),
        None => json!({}),
    };
    if let Some(reason) = snapshot.wait {
        output["reason"] = json!(reason);
        return Ok(output);
    }
    let criteria = plan
        .tickets
        .into_iter()
        .map(|t| (t.number, t.criteria))
        .collect::<HashMap<_, _>>();
    let mut first_reason = None;
    for candidate in snapshot.candidates.ok_or_else(invalid)? {
        let reason = if let Some(mut selection) = candidate.selection {
            let list = criteria.get(&candidate.number).ok_or_else(invalid)?;
            if selection["number"].as_i64() != Some(candidate.number) {
                return Err(invalid());
            }
            if list.iter().all(|s| shareable(s)) {
                selection["criteria"] = json!(list);
                output["scope"] = scope;
                output["selection"] = selection;
                return Ok(output);
            }
            "the approved criteria require redaction before sharing".to_owned()
        } else {
            candidate.reason.ok_or_else(invalid)?
        };
        first_reason.get_or_insert_with(|| {
            format!(
                "No eligible issue. Highest-priority issue #{}: {reason}",
                candidate.number
            )
        });
    }
    output["reason"] = json!(
        first_reason.unwrap_or_else(|| "No requested issue belongs to the approved plan".into())
    );
    Ok(output)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn skips_a_candidate_with_sensitive_criteria_and_preserves_the_first_blocker() {
        let plan = json!({"title":"Deliver","milestone":"Ship","tickets":[
            {"number":1,"priority":1,"criteria":["Cookie: TEST-ONLY-SECRET"],"verification":"Run tests","title":"One","estimateHours":1},
            {"number":2,"priority":2,"criteria":["Total is rounded"],"verification":"Run checks","title":"Two","estimateHours":1}
        ]});
        let mut snapshot = json!({"scope":{"planId":"p"},"plan":plan,"candidates":[
            {"number":1,"selection":{"number":1}},{"number":2,"selection":{"number":2}}]});
        let result = project(snapshot.clone()).unwrap();
        assert_eq!(result["selection"]["criteria"], json!(["Total is rounded"]));
        assert!(!result.to_string().contains("TEST-ONLY"));
        snapshot["candidates"].as_array_mut().unwrap().pop();
        let result = project(snapshot.clone()).unwrap();
        assert!(result["reason"].as_str().unwrap().contains("issue #1"));
        assert!(!result.to_string().contains("TEST-ONLY"));
        snapshot["plan"]["tickets"][0]["dependencies"] = json!([1]);
        assert!(project(snapshot).is_err());
    }
}
