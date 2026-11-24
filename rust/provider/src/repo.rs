// SPDX-License-Identifier: MPL-2.0
use crate::client::{Client, Error, Result, escape_component};
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{Value, json};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

#[derive(Clone, Copy, PartialEq, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Scope {
    Issues,
    ReadIssue,
    Discover,
    Draft,
    Inspection,
    QueueRead,
    Queue,
}

pub struct Repository {
    pub client: Client,
    pub token: String,
    pub name: String,
    pub path: String,
    pub id: i64,
    pub scope: Scope,
    pub installation: i64,
    pub(crate) app_id: i64,
    bot: String,
}

pub fn oid(s: &str) -> bool {
    [40, 64].contains(&s.len())
        && s.bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}
pub fn branch(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 160
        && s.as_bytes()[0].is_ascii_alphanumeric()
        && !s.contains("..")
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._/-".contains(&b))
        && s.split('/').all(|part| {
            !part.is_empty()
                && !part.starts_with('.')
                && !part.ends_with('.')
                && !part.ends_with(".lock")
        })
}
fn valid_name(s: &str) -> bool {
    let Some((owner, name)) = s.split_once('/') else {
        return false;
    };
    (1..=39).contains(&owner.len())
        && owner.as_bytes()[0].is_ascii_alphanumeric()
        && owner
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-')
        && (1..=100).contains(&name.len())
        && !matches!(name, "." | "..")
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
}

#[derive(Default, Deserialize, Serialize)]
#[serde(default)]
pub struct Issue {
    pub id: i64,
    pub number: i64,
    pub title: Option<String>,
    pub body: Option<String>,
    pub state: String,
    pub updated_at: String,
    #[serde(
        skip_serializing_if = "Option::is_none",
        deserialize_with = "present_field"
    )]
    pub pull_request: Option<Value>,
    user: Option<Author>,
    performed_via_github_app: Option<AppIdentity>,
}
// GitHub distinguishes issues from pull requests by field presence. Treat
// even an explicitly null pull_request as an unverified issue identity.
fn present_field<'de, D: Deserializer<'de>>(d: D) -> std::result::Result<Option<Value>, D::Error> {
    Value::deserialize(d).map(Some)
}

#[derive(Deserialize, Serialize)]
struct Author {
    #[serde(rename = "type", default)]
    kind: String,
    #[serde(default)]
    login: String,
}
#[derive(Deserialize, Serialize)]
struct AppIdentity {
    id: i64,
}

impl Repository {
    pub fn open(
        client: Client,
        installation: i64,
        id: i64,
        name: String,
        scope: Scope,
    ) -> Result<Self> {
        if installation < 1
            || (if scope == Scope::Discover {
                id != 0
            } else {
                id < 1
            })
            || !valid_name(&name)
        {
            return Err(Error::Invalid);
        }
        let permissions = match scope {
            Scope::Discover => json!({"metadata":"read"}),
            Scope::Issues => json!({"issues":"write"}),
            Scope::ReadIssue => json!({"issues":"read"}),
            Scope::Draft => json!({"contents":"read","issues":"read","pull_requests":"write"}),
            Scope::QueueRead => {
                json!({"contents":"read","pull_requests":"read","administration":"read","merge_queues":"read","checks":"read"})
            }
            Scope::Queue => {
                json!({"contents":"read","pull_requests":"read","administration":"read","merge_queues":"write","checks":"write"})
            }
            Scope::Inspection => {
                json!({"contents":"read","issues":"read","pull_requests":"read","checks":"read","statuses":"read"})
            }
        };
        let jwt = client.jwt()?;
        #[derive(Deserialize)]
        struct App {
            id: i64,
            slug: String,
        }
        let app: App = client.request(&jwt, "/app", None, 200)?;
        if app.id < 1 || app.slug.is_empty() {
            return Err(Error::Response);
        }
        #[derive(Deserialize)]
        struct Auth {
            token: String,
        }
        let (scope_request, identity_route) = if scope == Scope::Discover {
            let (_, short_name) = name.split_once('/').ok_or(Error::Invalid)?;
            (
                json!({"repositories":[short_name],"permissions":permissions}),
                format!("/repos/{name}"),
            )
        } else {
            (
                json!({"repository_ids":[id],"permissions":permissions}),
                format!("/repositories/{id}"),
            )
        };
        let auth: Auth = client.request(
            &jwt,
            &format!("/app/installations/{installation}/access_tokens"),
            Some(&scope_request),
            201,
        )?;
        if auth.token.is_empty() {
            return Err(Error::Response);
        }
        #[derive(Deserialize)]
        struct Identity {
            id: i64,
            full_name: String,
        }
        let identity: Identity = client.request(&auth.token, &identity_route, None, 200)?;
        if identity.id <= 0
            || (id != 0 && identity.id != id)
            || !identity.full_name.eq_ignore_ascii_case(&name)
        {
            return Err(Error::Response);
        }
        Ok(Self {
            client,
            token: auth.token,
            path: format!("/repos/{name}"),
            name,
            id: identity.id,
            scope,
            installation,
            app_id: app.id,
            bot: format!("{}[bot]", app.slug),
        })
    }
    pub fn get(&self, number: i64) -> Result<Issue> {
        if number < 1 {
            return Err(Error::Invalid);
        }
        let i: Issue = self.client.request(
            &self.token,
            &format!("{}/issues/{number}", self.path),
            None,
            200,
        )?;
        if i.id < 1 || i.number != number || i.pull_request.is_some() {
            return Err(Error::SubjectChanged);
        }
        Ok(i)
    }
    pub fn create(&self, title: &str, body: &str) -> Result<Issue> {
        let i: Issue = self.client.request(
            &self.token,
            &format!("{}/issues", self.path),
            Some(&json!({"title":title,"body":body})),
            201,
        )?;
        if i.id < 1 || i.number < 1 || i.pull_request.is_some() {
            return Err(Error::Response);
        }
        Ok(i)
    }
    pub fn find(&self, marker: &str, since: &str) -> Result<Option<Issue>> {
        OffsetDateTime::parse(since, &Rfc3339).map_err(|_| Error::Invalid)?;
        let suffix = format!("\n{marker}");
        let mut found: Option<Issue> = None;
        for page in 1..=10 {
            let route = format!(
                "{}/issues?state=all&sort=created&direction=desc&per_page=100&page={page}&since={}",
                self.path,
                escape_component(since)
            );
            let issues: Vec<Issue> = self.client.request(&self.token, &route, None, 200)?;
            let n = issues.len();
            for issue in issues {
                if issue.id < 1
                    || issue.number < 1
                    || issue.pull_request.is_some()
                    || !issue.body.as_deref().unwrap_or_default().ends_with(&suffix)
                {
                    continue;
                }
                let ours = match issue.performed_via_github_app.as_ref() {
                    Some(app) => app.id == self.app_id,
                    None => issue.user.as_ref().is_some_and(|u| {
                        u.kind == "Bot" && u.login.eq_ignore_ascii_case(&self.bot)
                    }),
                };
                if !ours {
                    continue;
                }
                if found.as_ref().is_some_and(|i| i.id != issue.id) {
                    return Err(Error::Response);
                }
                found = Some(issue);
            }
            if n < 100 {
                return Ok(found);
            }
        }
        Err(Error::Response)
    }
    pub fn check_draft(
        &self,
        head: &str,
        candidate: &str,
        target: &str,
        base: &str,
        number: i64,
        updated: &str,
    ) -> Result<()> {
        if !branch(head)
            || !branch(target)
            || head == target
            || base == candidate
            || !oid(candidate)
            || !oid(base)
        {
            return Err(Error::SubjectChanged);
        }
        let issue = self.get(number)?;
        let then = OffsetDateTime::parse(updated, &Rfc3339).map_err(|_| Error::SubjectChanged)?;
        let actual = OffsetDateTime::parse(&issue.updated_at, &Rfc3339)
            .map_err(|_| Error::SubjectChanged)?;
        if issue.state != "open" || actual != then {
            return Err(Error::SubjectChanged);
        }
        for (name, sha) in [(head, candidate), (target, base)] {
            let data: Value = self.client.request(
                &self.token,
                &format!("{}/git/ref/heads/{name}", self.path),
                None,
                200,
            )?;
            if data["ref"] != format!("refs/heads/{name}")
                || data["object"]["type"] != "commit"
                || data["object"]["sha"] != sha
            {
                return Err(Error::SubjectChanged);
            }
        }
        Ok(())
    }
    pub fn find_draft(&self, head: &str, target: &str, marker: &str) -> Result<Option<Draft>> {
        if !branch(head) || !branch(target) {
            return Err(Error::Invalid);
        }
        let owner = self.name.split_once('/').ok_or(Error::Invalid)?.0;
        let url = format!(
            "{}/pulls?state=all&per_page=100&base={}&head={}",
            self.path,
            escape_component(target),
            escape_component(&format!("{owner}:{head}"))
        );
        let drafts: Vec<Draft> = self.client.request(&self.token, &url, None, 200)?;
        if drafts.len() >= 100 {
            return Err(Error::Response);
        }
        let suffix = format!("\n{marker}");
        let mut found = None;
        for draft in drafts {
            if !draft.matches(self.id, head, target) {
                continue;
            }
            if draft.number > 0
                && draft
                    .user
                    .as_ref()
                    .is_some_and(|u| u.kind == "Bot" && u.login.eq_ignore_ascii_case(&self.bot))
                && draft.body.as_deref().unwrap_or_default().ends_with(&suffix)
            {
                if found.is_some() {
                    return Err(Error::SubjectChanged);
                }
                found = Some(draft);
            } else if draft.state == "open" {
                return Err(Error::SubjectChanged);
            }
        }
        Ok(found)
    }
    pub fn create_draft(&self, head: &str, target: &str, title: &str, body: &str) -> Result<Draft> {
        if !branch(head) || !branch(target) || head == target {
            return Err(Error::Invalid);
        }
        let draft:Draft=self.client.request(&self.token,&format!("{}/pulls",self.path),Some(&json!({"head":head,"base":target,"title":title,"body":body,"draft":true,"maintainer_can_modify":false})),201)?;
        if draft.number < 1 || !draft.draft || !draft.matches(self.id, head, target) {
            return Err(Error::Response);
        }
        Ok(draft)
    }
}

#[derive(Deserialize, Serialize)]
pub struct Draft {
    number: i64,
    #[serde(default)]
    body: Option<String>,
    #[serde(default)]
    state: String,
    #[serde(default)]
    draft: bool,
    user: Option<Author>,
    head: BranchRef,
    base: BranchRef,
}
#[derive(Deserialize, Serialize)]
struct BranchRef {
    #[serde(rename = "ref")]
    name: String,
    #[serde(default)]
    sha: String,
    repo: Option<AppIdentity>,
}
impl Draft {
    fn matches(&self, id: i64, head: &str, target: &str) -> bool {
        self.head.name == head
            && self.base.name == target
            && self.head.repo.as_ref().is_some_and(|r| r.id == id)
            && self.base.repo.as_ref().is_some_and(|r| r.id == id)
    }
}
