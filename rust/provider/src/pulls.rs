// SPDX-License-Identifier: MPL-2.0
use crate::client::{Error, Result, escape_component};
use crate::repo::{Repository, branch, oid};
use ring::digest::{SHA256, digest};
use serde_json::{Value, json, value::RawValue};
use std::collections::BTreeMap;

fn text(v: &Value) -> &str {
    v.as_str().unwrap_or_default()
}

fn go_escape(s: String) -> String {
    s.replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

fn hash(s: &str) -> String {
    let d = digest(&SHA256, s.as_bytes());
    d.as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

// Match encoding/json.Marshal(map[string]json.RawMessage) used for already
// stored evidence: top-level keys sorted; nested member order and number spellings
// preserved. Compact whitespace outside strings and retain Go's HTML escaping.
fn rule_definition(raw: &RawValue) -> Option<String> {
    let fields: BTreeMap<String, Box<RawValue>> = serde_json::from_str(raw.get()).ok()?;
    let json = serde_json::to_string(&fields).ok()?;
    let mut quoted = false;
    let mut escaped = false;
    let mut compact = String::with_capacity(json.len());
    for ch in json.chars() {
        if !quoted && ch.is_ascii_whitespace() {
            continue;
        }
        compact.push(ch);
        if escaped {
            escaped = false;
        } else if quoted && ch == '\\' {
            escaped = true;
        } else if ch == '"' {
            quoted = !quoted;
        }
    }
    Some(go_escape(compact))
}

impl Repository {
    fn rules(&self, target: &str) -> Option<Value> {
        if !branch(target) {
            return None;
        }
        let mut definitions = Vec::new();
        let (mut queue, mut checks, mut bound) = (false, 0, 0);
        for page in 1..=5 {
            let route = format!(
                "{}/rules/branches/{}?per_page=100&page={page}",
                self.path,
                escape_component(target)
            );
            let rules: Vec<Box<RawValue>> =
                self.client.request(&self.token, &route, None, 200).ok()?;
            for raw in &rules {
                let rule: Value = serde_json::from_str(raw.get()).ok()?;
                match rule["type"].as_str()? {
                    "" => return None,
                    "merge_queue" => queue = true,
                    "required_status_checks" => {
                        let list = &rule["parameters"]["required_status_checks"];
                        if !list.is_null() {
                            for check in list.as_array()? {
                                if text(&check["context"]).is_empty() {
                                    return None;
                                }
                                checks += 1;
                                if check["integration_id"].as_i64().is_some_and(|n| n > 0) {
                                    bound += 1;
                                }
                            }
                        }
                    }
                    _ => {}
                }
                definitions.push(rule_definition(raw)?);
            }
            if rules.len() < 100 {
                definitions.sort();
                let encoded = if definitions.is_empty() {
                    "null".to_owned()
                } else {
                    go_escape(serde_json::to_string(&definitions).ok()?)
                };
                return Some(
                    json!({"digest":hash(&encoded),"queueRequired":queue,"requiredChecks":checks,"appBoundChecks":bound}),
                );
            }
        }
        None
    }
    pub fn inspect(&self, number: i64, issue_number: i64) -> Result<Value> {
        if !(1..=i32::MAX as i64).contains(&number)
            || !(1..=i32::MAX as i64).contains(&issue_number)
            || number == issue_number
        {
            return Err(Error::Invalid);
        }
        let (owner, name) = self.name.split_once('/').ok_or(Error::Invalid)?;
        let request = json!({"query":include_str!("inspection.graphql"),"variables":{"owner":owner,"name":name,"pr":number,"issue":issue_number}});
        let response: Value = self
            .client
            .request(&self.token, "/graphql", Some(&request), 200)?;
        let repo = &response["data"]["repository"];
        let (p, i) = (&repo["pullRequest"], &repo["issue"]);
        let (head, target) = (text(&p["headRefOid"]), text(&p["baseRefName"]));
        if response["errors"].as_array().is_some_and(|a| !a.is_empty())
            || !text(&repo["nameWithOwner"]).eq_ignore_ascii_case(&self.name)
            || p["number"] != number
            || i["number"] != issue_number
            || text(&i["id"]).is_empty()
            || !oid(head)
            || target.is_empty()
        {
            return Err(Error::Response);
        }
        let base = text(&p["baseRef"]["target"]["oid"]);
        let review = text(&p["reviewDecision"]);
        let criteria =
            go_escape(json!([text(&i["id"]), text(&i["title"]), text(&i["body"])]).to_string());
        let mut obs = json!({"head":head,"target":target,"base":if oid(base){base}else{""},"criteria":hash(&criteria),
            "title":text(&p["title"]),"state":text(&p["state"]),"issueState":text(&i["state"]),
            "review":if review.is_empty(){"UNKNOWN"}else{review},"checks":"UNKNOWN","checkCount":0,"draft":p["isDraft"].as_bool().unwrap_or(false)});
        let mut queue = json!({"ownerType":text(&repo["owner"]["__typename"]),"present":!text(&p["mergeQueue"]["id"]).is_empty()});
        if let Some(rule) = self.rules(target) {
            queue["rules"] = rule
        }
        let entry = &p["mergeQueueEntry"];
        if entry.is_object() {
            queue["entryState"] = json!(text(&entry["state"]));
            if let Some(position) = entry["position"].as_i64() {
                queue["position"] = json!(position)
            }
            for (field, source) in [("entryHead", "headCommit"), ("entryBase", "baseCommit")] {
                let sha = text(&entry[source]["oid"]);
                if oid(sha) {
                    queue[field] = json!(sha)
                }
            }
        }
        obs["queue"] = queue;
        let merged = text(&p["mergeCommit"]["oid"]);
        if p["state"] == "MERGED" && oid(merged) {
            obs["mergedSha"] = json!(merged)
        }
        if let Some([node]) = p["commits"]["nodes"].as_array().map(Vec::as_slice) {
            let commit = &node["commit"];
            let rollup = &commit["statusCheckRollup"];
            if commit["oid"] == head
                && let Some(count) = rollup["contexts"]["totalCount"].as_i64().filter(|n| *n > 0)
                && let Some(check) = rollup["state"].as_str()
            {
                obs["checks"] = json!(check);
                obs["checkCount"] = json!(count);
            }
        }
        Ok(obs)
    }
}
