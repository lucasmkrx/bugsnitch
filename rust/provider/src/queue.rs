// SPDX-License-Identifier: MPL-2.0
use crate::client::{Error, Result, escape_component};
use crate::repo::{Repository, Scope, branch, oid};
use serde_json::{Value, json};

const GUARD: &str = "Bugsnitch queue authorization";

fn required(rules: &[Value], app: i64, label: &str) -> (bool, bool) {
    let mut guard = false;
    let mut other = false;
    for rule in rules
        .iter()
        .filter(|r| r["type"] == "required_status_checks")
    {
        for check in rule["parameters"]["required_status_checks"]
            .as_array()
            .into_iter()
            .flatten()
        {
            let source = check["integration_id"].as_i64().unwrap_or(0);
            if check["context"] == label && source == app {
                guard = true;
            }
            if check["context"]
                .as_str()
                .is_some_and(|c| !c.is_empty() && c != label)
                && source > 0
                && source != app
            {
                other = true;
            }
        }
    }
    (guard, other)
}

fn covers(v: &Value, app: i64, repo: &str, pattern: &str, label: &str) -> bool {
    let conditions = &v["conditions"];
    v["target"] == "branch"
        && v["enforcement"] == "active"
        && v["source_type"] == "Repository"
        && v["source"]
            .as_str()
            .is_some_and(|s| s.eq_ignore_ascii_case(repo))
        && v["bypass_actors"].as_array().is_some_and(Vec::is_empty)
        && conditions.as_object().is_some_and(|m| m.len() == 1)
        && conditions["ref_name"]["include"] == json!([pattern])
        && conditions["ref_name"]["exclude"] == json!([])
        && v["rules"].as_array().is_some_and(|rules| {
            required(rules, app, label).0
                && rules.iter().any(|r| {
                    r["type"] == "merge_queue" && r["parameters"]["merge_method"] == "MERGE"
                })
        })
}

impl Repository {
    fn guard_coverage(&self, target: &str, local: &str) -> Result<(bool, bool)> {
        let list: Vec<Value> = self.client.request(
            &self.token,
            &format!("{}/rulesets?includes_parents=false&per_page=100", self.path),
            None,
            200,
        )?;
        // GitHub omits bypass_actors from read-only tokens. The isolated
        // rules auditor uses this token exclusively for a ruleset GET.
        let jwt = self.client.jwt()?;
        let response: Value = self.client.request(
            &jwt,
            &format!("/app/installations/{}/access_tokens", self.installation),
            Some(&json!({"repository_ids":[self.id],"permissions":{"administration":"write"}})),
            201,
        )?;
        let audit_token = response["token"]
            .as_str()
            .filter(|s| !s.is_empty())
            .ok_or(Error::Response)?;
        // A single qualifying repository ruleset is sufficient; other
        // rulesets can only add restrictions.
        let (mut all, mut named) = (false, false);
        for row in list
            .iter()
            .filter(|r| r["enforcement"] == "active" && r["target"] == "branch")
            .take(12)
        {
            let id = row["id"]
                .as_i64()
                .filter(|n| *n > 0)
                .ok_or(Error::Response)?;
            let v: Value = self.client.request(
                audit_token,
                &format!("{}/rulesets/{id}", self.path),
                None,
                200,
            )?;
            all |= covers(&v, self.app_id, &self.name, "~ALL", GUARD);
            named |= covers(
                &v,
                self.app_id,
                &self.name,
                &format!("refs/heads/{target}"),
                local,
            );
        }
        Ok((all, named))
    }

    pub fn queue_subject(&self, n: i64, target: &str) -> Result<Value> {
        if !(1..=i32::MAX as i64).contains(&n) || !branch(target) {
            return Err(Error::Invalid);
        }
        let (owner, name) = self.name.split_once('/').ok_or(Error::Invalid)?;
        let q = json!({"query":"query QueueSubject($owner:String!,$name:String!,$number:Int!) { repository(owner:$owner,name:$name){databaseId pullRequest(number:$number){id number headRefOid headRepository{databaseId} baseRefName baseRef{target{oid}} mergeQueue{id} mergeQueueEntry{id}}}}","variables":{"owner":owner,"name":name,"number":n}});
        let v: Value = self
            .client
            .request(&self.token, "/graphql", Some(&q), 200)?;
        let repo = &v["data"]["repository"];
        let p = &repo["pullRequest"];
        if v["errors"].as_array().is_some_and(|a| !a.is_empty())
            || repo["databaseId"] != self.id
            || p["number"] != n
            || p["headRepository"]["databaseId"] != self.id
            || p["baseRefName"] != target
            || !p["id"].is_string()
        {
            return Err(Error::SubjectChanged);
        }
        let rules: Vec<Value> = self.client.request(
            &self.token,
            &format!(
                "{}/rules/branches/{}?per_page=100&page=1",
                self.path,
                escape_component(target)
            ),
            None,
            200,
        )?;
        let local = format!("Bugsnitch queue / {target}");
        let (guard, ci) = required(&rules, self.app_id, GUARD);
        let (named, _) = required(&rules, self.app_id, &local);
        let queue = rules
            .iter()
            .any(|r| r["type"] == "merge_queue" && r["parameters"]["merge_method"] == "MERGE");
        let (all, on_target) = self.guard_coverage(target, &local)?;
        let enqueue = all && guard;
        Ok(
            json!({"id":p["id"],"head":p["headRefOid"],"base":p["baseRef"]["target"]["oid"],"target":p["baseRefName"],
            "inQueue":p["mergeQueueEntry"]["id"].is_string(),"canEnqueue":enqueue,"label":if enqueue { GUARD }else{&local},"guarded":(enqueue || (on_target && named)) && ci && queue && rules.len()<100 && p["mergeQueue"]["id"].is_string()}),
        )
    }

    pub fn queue_group(
        &self,
        group: &str,
        group_ref: &str,
        base: &str,
        head: &str,
    ) -> Result<bool> {
        if ![group, base, head].iter().all(|s| oid(s))
            || !group_ref.starts_with("gh-readonly-queue/")
            || !branch(group_ref)
            || group == base
            || group == head
            || head == base
        {
            return Err(Error::Invalid);
        }
        let current: Value = self.client.request(
            &self.token,
            &format!(
                "{}/git/ref/heads/{}",
                self.path,
                escape_component(group_ref)
            ),
            None,
            200,
        )?;
        let c: Value = self.client.request(
            &self.token,
            &format!("{}/git/commits/{group}", self.path),
            None,
            200,
        )?;
        Ok(current["ref"] == format!("refs/heads/{group_ref}")
            && current["object"]["type"] == "commit"
            && current["object"]["sha"] == group
            && c["sha"] == group
            && c["parents"]
                .as_array()
                .is_some_and(|p| p.len() == 2 && p[0]["sha"] == base && p[1]["sha"] == head))
    }

    pub fn queue_check(&self, sha: &str, request: &str, label: &str) -> Result<()> {
        if self.scope != Scope::Queue
            || (label != GUARD && !label.strip_prefix("Bugsnitch queue / ").is_some_and(branch))
            || !oid(sha)
            || request.len() != 64
            || !request.bytes().all(|b| b.is_ascii_hexdigit())
        {
            return Err(Error::Invalid);
        }
        let checks: Value = self.client.request(
            &self.token,
            &format!(
                "{}/commits/{sha}/check-runs?check_name={}&filter=latest&per_page=100",
                self.path,
                escape_component(label)
            ),
            None,
            200,
        )?;
        if checks["check_runs"]
            .as_array()
            .ok_or(Error::Response)?
            .iter()
            .any(|c| {
                c["app"]["id"] == self.app_id
                    && c["head_sha"] == sha
                    && c["external_id"] == request
                    && c["name"] == label
                    && c["conclusion"] == "success"
            })
        {
            return Ok(());
        }
        let result:Value=self.client.request(&self.token,&format!("{}/check-runs",self.path),Some(&json!({"name":label,"head_sha":sha,"external_id":request,"status":"completed","conclusion":"success","output":{"title":"Scoped queue authorization","summary":"Bugsnitch verified the current authorized delivery subject."}})),201)?;
        if result["app"]["id"] != self.app_id
            || result["head_sha"] != sha
            || result["name"] != label
            || result["conclusion"] != "success"
        {
            return Err(Error::Response);
        }
        Ok(())
    }

    pub fn enqueue(&self, n: i64, base: &str, target: &str, head: &str) -> Result<()> {
        if self.scope != Scope::Queue || !oid(base) || !oid(head) {
            return Err(Error::Invalid);
        }
        let current = self.queue_subject(n, target)?;
        if current["canEnqueue"] != true
            || current["guarded"] != true
            || current["base"] != base
            || current["head"] != head
        {
            return Err(Error::SubjectChanged);
        }
        if current["inQueue"] == true {
            return Ok(());
        }
        let q = json!({"query":"mutation BugsnitchEnqueue($input:EnqueuePullRequestInput!){enqueuePullRequest(input:$input){mergeQueueEntry{id}}}",
            "variables":{"input":{"pullRequestId":current["id"],"expectedHeadOid":head}}});
        let v: Value = self
            .client
            .request(&self.token, "/graphql", Some(&q), 200)?;
        if v["errors"].as_array().is_some_and(|a| !a.is_empty())
            || !v["data"]["enqueuePullRequest"]["mergeQueueEntry"]["id"].is_string()
        {
            return Err(Error::Response);
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn all_destinations_need_a_named_app_check_without_bypass() {
        let rule = json!({"type":"required_status_checks","parameters":{"required_status_checks":[{"context":GUARD,"integration_id":7},{"context":"build","integration_id":9}]}});
        let v = json!({"target":"branch","source":"example/test","source_type":"Repository","enforcement":"active","conditions":{"ref_name":{"include":["~ALL"],"exclude":[]}},"bypass_actors":[],"rules":[rule.clone(),{"type":"merge_queue","parameters":{"merge_method":"MERGE"}}]});
        assert!(covers(&v, 7, "example/test", "~ALL", GUARD));
        assert_eq!(required(&[rule], 7, GUARD), (true, true));
        for (k, bad) in [
            ("bypass_actors", json!(null)),
            ("bypass_actors", json!([{"actor_id":7}])),
            (
                "conditions",
                json!({"ref_name":{"include":["~ALL"],"exclude":["refs/heads/production"]}}),
            ),
            ("enforcement", json!("evaluate")),
            ("rules", json!([])),
        ] {
            let mut changed = v.clone();
            changed[k] = bad;
            assert!(!covers(&changed, 7, "example/test", "~ALL", GUARD));
        }
    }
}
