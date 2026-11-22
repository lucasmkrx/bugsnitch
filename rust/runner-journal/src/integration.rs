// SPDX-License-Identifier: MPL-2.0
use crate::worktree::{
    check_association, check_clean, check_origin, ensure_removable, git, private_parent, valid_id,
    valid_sha,
};
use serde::Deserialize;
use serde_json::{Value, json};
use std::{
    fs,
    io::{ErrorKind, Read, Write},
    path::{Path, PathBuf},
};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Request {
    version: u8,
    operation: Operation,
    home: PathBuf,
    project: PathBuf,
    repository: String,
    target_ref: String,
    identity: IntegrationValidation,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct IntegrationValidation {
    attempt_id: String,
    check_id: String,
    candidate_sha: String,
    #[serde(default)]
    target_sha: String,
    #[serde(default)]
    combined_sha: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
enum Operation {
    Resolve,
    Combine,
    Verify,
    Cleanup,
}

struct Check {
    request: Request,
    tree: PathBuf,
    candidate_ref: String,
}

fn blocked(status: &str, reason: &str) -> Value {
    json!({"status": status, "reason": reason})
}

fn detached_head(project: &Path, tree: &Path) -> Result<String, String> {
    check_association(project, tree)?;
    if git(tree, &["rev-parse", "--symbolic-full-name", "HEAD"])? != "HEAD" {
        return Err("integration checkout is no longer detached".into());
    }
    git(tree, &["rev-parse", "--verify", "HEAD^{commit}"])
}

impl IntegrationValidation {
    // Unlike files inside the checkout this receipt survives repository checkout
    // operations and cannot be supplied by a tracked file in the candidate.
    fn ownership(&self, tree: &Path, create: bool) -> Result<(), String> {
        let key = format!(
            "{} {} {} {}\n",
            self.attempt_id, self.check_id, self.target_sha, self.candidate_sha
        );
        let admin = git(tree, &["rev-parse", "--absolute-git-dir"])?;
        let marker = PathBuf::from(admin).join("bugsnitch-integration-owner");
        if create {
            let mut file = fs::File::create_new(marker)
                .map_err(|_| "cannot record integration checkout ownership")?;
            file.write_all(key.as_bytes())
                .and_then(|()| file.sync_all())
                .map_err(|_| "cannot record integration checkout ownership")?;
        } else {
            let missing = "integration ownership receipt missing or changed; checkout preserved";
            if !fs::symlink_metadata(&marker).is_ok_and(|s| s.is_file()) {
                return Err(missing.into());
            }
            let mut bytes = Vec::new();
            fs::File::open(marker)
                .and_then(|f| f.take(512).read_to_end(&mut bytes))
                .map_err(|_| missing)?;
            if bytes != key.as_bytes() {
                return Err(missing.into());
            }
        }
        Ok(())
    }
}

pub(super) fn verify_launch(
    home: &Path,
    project: &Path,
    repo: &str,
    tree: &Path,
    id: &IntegrationValidation,
) -> Result<(), String> {
    if !valid_id(&id.check_id)
        || !valid_id(&id.attempt_id)
        || !valid_sha(&id.candidate_sha)
        || !valid_sha(&id.target_sha)
        || !valid_sha(&id.combined_sha)
    {
        return Err("invalid integration validation identity".into());
    }
    check_origin(project, repo)?;
    let parent = private_parent(home, "integration")?;
    if parent.starts_with(project) || project.starts_with(home) || parent.join(&id.check_id) != tree
    {
        return Err("integration validation location mismatch".into());
    }
    if detached_head(project, tree)? != id.combined_sha {
        return Err("combined revision changed before validation".into());
    }
    id.ownership(tree, false)?;
    check_clean(tree)
}

impl Check {
    fn open(request: Request) -> Result<Self, String> {
        if request.version != 1
            || !valid_id(&request.identity.attempt_id)
            || !valid_id(&request.identity.check_id)
            || !valid_sha(&request.identity.candidate_sha)
            || (!matches!(request.operation, Operation::Resolve)
                && !valid_sha(&request.identity.target_sha))
            || (matches!(request.operation, Operation::Verify | Operation::Cleanup)
                && !valid_sha(&request.identity.combined_sha))
        {
            return Err("invalid integration subject or protocol version".into());
        }
        if !(request.target_ref.starts_with("refs/heads/")
            || request.target_ref.starts_with("refs/remotes/origin/"))
        {
            return Err("use an explicit local branch or origin remote-tracking reference".into());
        }
        check_origin(&request.project, &request.repository)?;
        git(&request.project, &["check-ref-format", &request.target_ref])
            .map_err(|_| "invalid integration target")?;
        let parent = private_parent(&request.home, "integration")?;
        if parent.starts_with(&request.project) || request.project.starts_with(&request.home) {
            return Err("runner state and repository must use separate directories".into());
        }
        let tree = parent.join(&request.identity.check_id);
        let candidate_ref = format!("refs/heads/bugsnitch/{}", request.identity.attempt_id);
        Ok(Self {
            request,
            tree,
            candidate_ref,
        })
    }

    fn revision(&self, reference: &str) -> Result<String, String> {
        let commit = format!("{reference}^{{commit}}");
        git(&self.request.project, &["rev-parse", "--verify", &commit])
    }

    fn references_match(&self) -> Result<(), String> {
        if self.revision(&self.candidate_ref)? != self.request.identity.candidate_sha
            || self.revision(&self.request.target_ref)? != self.request.identity.target_sha
        {
            return Err("integration target or reviewed candidate changed".into());
        }
        Ok(())
    }

    fn combine(&self) -> Result<Value, String> {
        if self.references_match().is_err() {
            return Ok(blocked(
                "stale",
                "integration target or reviewed candidate changed",
            ));
        }
        match fs::symlink_metadata(&self.tree) {
            Err(e) if e.kind() == ErrorKind::NotFound => {}
            _ => return Err("integration destination already exists or is inaccessible".into()),
        }
        let path = self.tree.to_str().ok_or("invalid integration path")?;
        git(
            &self.request.project,
            &[
                "worktree",
                "add",
                "--detach",
                "--",
                path,
                &self.request.identity.target_sha,
            ],
        )?;
        if detached_head(&self.request.project, &self.tree)? != self.request.identity.target_sha {
            return Err("integration checkout does not match its recorded target".into());
        }
        self.request.identity.ownership(&self.tree, true)?;
        if git(
            &self.tree,
            &[
                "merge",
                "--no-ff",
                "--no-edit",
                "--no-autostash",
                &self.request.identity.candidate_sha,
            ],
        )
        .is_err()
        {
            return Ok(blocked(
                "merge_failed",
                "local integration failed; inspect retained worktree for conflicts",
            ));
        }
        let head = detached_head(&self.request.project, &self.tree)?;
        for parent in [
            &self.request.identity.target_sha,
            &self.request.identity.candidate_sha,
        ] {
            if git(&self.tree, &["merge-base", "--is-ancestor", parent, &head]).is_err() {
                return Ok(blocked(
                    "unverified",
                    "combined revision does not contain both selected commits",
                ));
            }
        }
        Ok(json!({"combinedSha": head}))
    }

    fn verify(&self) -> Result<Value, String> {
        if detached_head(&self.request.project, &self.tree)? != self.request.identity.combined_sha {
            return Ok(blocked("stale", "validation moved the combined candidate"));
        }
        self.request.identity.ownership(&self.tree, false)?;
        if check_clean(&self.tree).is_err() {
            return Ok(blocked(
                "unverified",
                "validation changed the integration checkout or its cleanliness could not be verified",
            ));
        }
        if self.references_match().is_err() {
            return Ok(blocked(
                "stale",
                "integration target or reviewed candidate changed",
            ));
        }
        Ok(json!({}))
    }

    fn cleanup(&self) -> Result<Value, String> {
        let archive = format!(
            "refs/bugsnitch/integration/{}",
            self.request.identity.check_id
        );
        let archived = self.revision(&archive).ok();
        match fs::symlink_metadata(&self.tree) {
            Err(e)
                if e.kind() == ErrorKind::NotFound
                    && archived.as_ref() == Some(&self.request.identity.combined_sha) =>
            {
                let path = self.tree.to_str().ok_or("invalid integration path")?;
                let registered = git(
                    &self.request.project,
                    &["worktree", "list", "--porcelain", "-z"],
                )?;
                if registered
                    .split('\0')
                    .any(|line| line.strip_prefix("worktree ") == Some(path))
                {
                    return Err("integration checkout missing with Git registration retained; inspect before cleanup".into());
                }
                return Ok(json!({"archiveRef": archive}));
            }
            Err(_) => {
                return Err(
                    "integration worktree missing or inaccessible; inspect its record".into(),
                );
            }
            Ok(_) => {}
        }
        if detached_head(&self.request.project, &self.tree)? != self.request.identity.combined_sha {
            return Err("integration revision changed; worktree preserved".into());
        }
        self.request.identity.ownership(&self.tree, false)?;
        ensure_removable(&self.tree)?;
        // Keep the tested merge reachable after Git removes the detached HEAD.
        // An empty expected old value creates a ref only if it does not exist.
        match archived {
            Some(value) if value != self.request.identity.combined_sha => {
                return Err("integration archive reference changed".into());
            }
            None => {
                git(
                    &self.request.project,
                    &[
                        "update-ref",
                        &archive,
                        &self.request.identity.combined_sha,
                        "",
                    ],
                )?;
            }
            _ => {}
        }
        git(
            &self.request.project,
            &[
                "worktree",
                "remove",
                "--",
                self.tree.to_str().ok_or("invalid integration path")?,
            ],
        )?;
        Ok(json!({"archiveRef": archive}))
    }
}

pub fn run(input: Value) -> Result<Value, String> {
    let request: Request =
        serde_json::from_value(input).map_err(|_| "invalid integration request")?;
    let check = Check::open(request)?;
    match check.request.operation {
        Operation::Resolve => {
            if check.revision(&check.candidate_ref)? != check.request.identity.candidate_sha {
                return Err("candidate branch changed; review evidence is stale".into());
            }
            Ok(json!({"targetSha": check.revision(&check.request.target_ref)?}))
        }
        Operation::Combine => check.combine(),
        Operation::Verify => check.verify(),
        Operation::Cleanup => check.cleanup(),
    }
}
