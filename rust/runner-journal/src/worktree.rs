// SPDX-License-Identifier: MPL-2.0
use serde::Deserialize;
use serde_json::Value;
use std::{
    env, fs,
    io::{ErrorKind, Read},
    path::{Path, PathBuf},
    process::{ChildStdout, Command, Stdio},
    sync::mpsc,
    thread,
    time::{Duration, Instant},
};

const GIT_OUTPUT_LIMIT: usize = 512 << 10;
#[cfg(windows)]
const NULL_FILE: &str = "NUL";
#[cfg(not(windows))]
const NULL_FILE: &str = "/dev/null";

/// Only the local, typed operation handlers construct these arguments.
pub(crate) fn git(dir: &Path, args: &[&str]) -> Result<String, String> {
    run_git(dir, args, |stdout| {
        let mut bytes = Vec::new();
        stdout
            .take((GIT_OUTPUT_LIMIT + 1) as u64)
            .read_to_end(&mut bytes)
            .map_err(|_| "reading Git output failed")?;
        if bytes.len() > GIT_OUTPUT_LIMIT {
            return Err("Git output exceeds artifact limit".into());
        }
        let mut output = String::from_utf8(bytes).map_err(|_| "Git output is not UTF-8")?;
        if output.ends_with('\n') {
            output.pop();
        }
        Ok(output)
    })
}

fn run_git<T: Send + 'static>(
    dir: &Path,
    args: &[&str],
    read_output: impl FnOnce(ChildStdout) -> Result<T, String> + Send + 'static,
) -> Result<T, String> {
    let failure = || format!("git {} failed", args[0]);
    let git_bin = env::var_os("BUGSNITCH_GIT")
        .map(PathBuf::from)
        .filter(|p| p.is_absolute())
        .ok_or("absolute Git executable required")?;
    let inherited_path = env::var_os("PATH").unwrap_or_default();
    let path = env::join_paths(env::split_paths(&inherited_path).filter(|p| p.is_absolute()))
        .map_err(|_| "invalid Git executable path")?;
    let mut command = Command::new(git_bin);
    #[cfg(any(target_os = "linux", target_os = "macos"))]
    {
        use std::os::unix::process::CommandExt;
        command.process_group(0);
    }
    let mut child = command
        .env_clear()
        .env("PATH", path)
        .env("GIT_CONFIG_GLOBAL", NULL_FILE)
        .env("GIT_CONFIG_NOSYSTEM", "1")
        .env("GIT_TERMINAL_PROMPT", "0")
        .env("GIT_LFS_SKIP_SMUDGE", "1")
        .env("LC_ALL", "C")
        .args([
            "-c",
            &format!("core.hooksPath={NULL_FILE}"),
            "-c",
            "commit.gpgsign=false",
            "-c",
            "user.name=Bugsnitch",
            "-c",
            "user.email=runner@bugsnitch.invalid",
            "-C",
        ])
        .arg(dir)
        .args(args)
        .stdin(Stdio::null())
        .stderr(Stdio::null())
        .stdout(Stdio::piped())
        .spawn()
        .map_err(|_| failure())?;
    let stdout = child.stdout.take().expect("piped git stdout");
    let (send, receive) = mpsc::sync_channel(1);
    thread::spawn(move || {
        let _ = send.send(read_output(stdout));
    });
    let deadline = Instant::now() + Duration::from_secs(60);
    // A repository filter may inherit Git's stdout. Bound pipe reads and child
    // exit independently, and never join a blocked pipe reader on timeout.
    let mut reaped = false;
    let result = (|| {
        let output = receive
            .recv_timeout(deadline.saturating_duration_since(Instant::now()))
            .map_err(|_| "git command timed out".to_string())??;
        loop {
            if let Some(status) = child.try_wait().map_err(|_| failure())? {
                reaped = true;
                if !status.success() {
                    return Err(failure());
                }
                return Ok(output);
            }
            if Instant::now() >= deadline {
                return Err("git command timed out".into());
            }
            thread::sleep(Duration::from_millis(2));
        }
    })();
    if result.is_err() && !reaped {
        // The group was created for this invocation. Its leader is still
        // owned and unreaped, which prevents PID reuse while signalling.
        #[cfg(any(target_os = "linux", target_os = "macos"))]
        unsafe {
            libc::kill(-(child.id() as libc::pid_t), libc::SIGKILL);
        }
        let _ = child.kill();
        let _ = child.wait();
    }
    result
}

pub(crate) fn valid_id(s: &str) -> bool {
    s.len() == 32 && lower_hex(s)
}
pub(crate) fn valid_sha(s: &str) -> bool {
    matches!(s.len(), 40 | 64) && lower_hex(s)
}
fn lower_hex(s: &str) -> bool {
    s.bytes()
        .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn as_str(p: &Path) -> Result<&str, String> {
    p.to_str().ok_or_else(|| "invalid checkout path".into())
}

fn canonical_dir(path: &Path) -> bool {
    path.is_absolute()
        && path.is_dir()
        && fs::canonicalize(path).is_ok_and(|p| p.as_os_str() == path.as_os_str())
}

pub(crate) fn check_origin(root: &Path, repository: &str) -> Result<(), String> {
    if !canonical_dir(root) || git(root, &["rev-parse", "--show-toplevel"])? != as_str(root)? {
        return Err("mapping must identify the canonical checkout root".into());
    }
    let remote = git(root, &["remote", "get-url", "origin"])?.to_ascii_lowercase();
    let remote = remote.strip_suffix(".git").unwrap_or(&remote);
    if [
        "git@github.com:",
        "https://github.com/",
        "ssh://git@github.com/",
    ]
    .iter()
    .any(|prefix| {
        remote
            .strip_prefix(prefix)
            .is_some_and(|repo| repo.eq_ignore_ascii_case(repository))
    }) {
        return Ok(());
    }
    Err("registered path and selected GitHub repository disagree".into())
}

pub(crate) fn private_parent(home: &Path, name: &str) -> Result<PathBuf, String> {
    if !canonical_dir(home) || !matches!(name, "worktrees" | "integration") {
        return Err("runner data directory changed".into());
    }
    let parent = home.join(name);
    let mut dir = fs::DirBuilder::new();
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        dir.mode(0o700);
    }
    match dir.create(&parent) {
        Ok(()) => (),
        Err(e) if e.kind() == ErrorKind::AlreadyExists => (),
        Err(_) => return Err("cannot create worktree parent".into()),
    }
    if !canonical_dir(&parent) {
        return Err("worktree parent changed".into());
    }
    Ok(parent)
}

/// Verify both directions of Git's registration. A copied .git pointer can
/// share the right common directory while referring to another worktree.
pub(crate) fn check_association(project: &Path, tree: &Path) -> Result<(), String> {
    if project == tree
        || !canonical_dir(tree)
        || git(tree, &["rev-parse", "--show-toplevel"])? != as_str(tree)?
    {
        return Err("worktree root mismatch".into());
    }
    let common_args = ["rev-parse", "--path-format=absolute", "--git-common-dir"];
    let common = git(project, &common_args)?;
    if git(tree, &common_args)? != common {
        return Err("worktree association mismatch".into());
    }
    let admin = PathBuf::from(git(tree, &["rev-parse", "--absolute-git-dir"])?);
    let registered = Path::new(&common).join("worktrees");
    if admin.parent() != Some(registered.as_path()) || !canonical_dir(&admin) {
        return Err("worktree registration mismatch".into());
    }
    let pointer = tree.join(".git");
    if !fs::symlink_metadata(&pointer).is_ok_and(|s| s.is_file()) {
        return Err("worktree registration mismatch".into());
    }
    let backpointer = admin.join("gitdir");
    if !fs::symlink_metadata(&backpointer).is_ok_and(|s| s.is_file()) {
        return Err("worktree backpointer unavailable".into());
    }
    let mut back = String::new();
    let file = fs::File::open(backpointer).map_err(|_| "worktree backpointer unavailable")?;
    file.take(65537)
        .read_to_string(&mut back)
        .map_err(|_| "worktree backpointer invalid")?;
    let target = admin.join(back.trim_end_matches(['\n', '\r']));
    if back.len() > 65536 || !fs::canonicalize(target).is_ok_and(|p| p == pointer) {
        return Err("worktree backpointer mismatch".into());
    }
    Ok(())
}

pub(crate) fn check_clean(tree: &Path) -> Result<(), String> {
    if !git(
        tree,
        &[
            "status",
            "--porcelain",
            "--untracked-files=all",
            "--ignore-submodules=none",
        ],
    )?
    .is_empty()
    {
        return Err("worktree changed; retained for inspection".into());
    }
    // Git status can hide local edits to paths marked skip-worktree or
    // assume-unchanged. Preserve these checkouts for manual inspection.
    run_git(tree, &["ls-files", "-v", "-z"], |mut stdout| {
        let mut buf = [0; 8192];
        let mut at_record_start = true;
        loop {
            let n = stdout
                .read(&mut buf)
                .map_err(|_| "reading Git index flags failed")?;
            if n == 0 {
                return Ok(());
            }
            for byte in &buf[..n] {
                if at_record_start && (byte.is_ascii_lowercase() || *byte == b'S') {
                    return Err(
                        "worktree has paths excluded from change detection; preserved".into(),
                    );
                }
                at_record_start = *byte == 0;
            }
        }
    })
}

pub(crate) fn ensure_removable(tree: &Path) -> Result<(), String> {
    check_clean(tree)?;
    // An empty patch can leave an active Git operation with no dirty files.
    // Removing this checkout would discard the owner's pending operation.
    let admin = PathBuf::from(git(tree, &["rev-parse", "--absolute-git-dir"])?);
    for name in [
        "MERGE_HEAD",
        "MERGE_AUTOSTASH",
        "CHERRY_PICK_HEAD",
        "REVERT_HEAD",
        "BISECT_START",
        "rebase-apply",
        "rebase-merge",
        "sequencer",
        "index.lock",
    ] {
        match fs::symlink_metadata(admin.join(name)) {
            Err(error) if error.kind() == ErrorKind::NotFound => (),
            _ => return Err("worktree has pending Git operation state; preserved".into()),
        }
    }
    if !git(tree, &["ls-files", "--others", "--directory"])?.is_empty() {
        return Err("worktree contains untracked or ignored files; preserved".into());
    }
    Ok(())
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct AttemptLocation {
    home: PathBuf,
    project: PathBuf,
    repository: String,
    id: String,
    branch: String,
    worktree: PathBuf,
    base: String,
}

impl AttemptLocation {
    pub(crate) fn verify_launch(
        &self,
        expected_tree: &Path,
        expected_sha: &str,
    ) -> Result<(), String> {
        if self.worktree.as_os_str() != expected_tree.as_os_str() {
            return Err("validation directory differs from the recorded worktree".into());
        }
        self.checked()?.inspect_at(expected_sha)?;
        check_clean(&self.worktree)
    }
    fn checked(&self) -> Result<&Self, String> {
        if !valid_id(&self.id)
            || !valid_sha(&self.base)
            || self.branch != format!("bugsnitch/{}", self.id)
            || self.worktree.as_os_str() != self.home.join("worktrees").join(&self.id).as_os_str()
            || self.home.starts_with(&self.project)
            || self.project.starts_with(&self.home)
        {
            return Err("attempt location differs from registered identity".into());
        }
        private_parent(&self.home, "worktrees")?;
        check_origin(&self.project, &self.repository)?;
        Ok(self)
    }
    fn inspect(&self) -> Result<(), String> {
        check_association(&self.project, &self.worktree)?;
        if git(&self.worktree, &["symbolic-ref", "--short", "HEAD"])? != self.branch {
            return Err("worktree branch mismatch".into());
        }
        Ok(())
    }
    fn inspect_at(&self, sha: &str) -> Result<(), String> {
        self.inspect()?;
        if !valid_sha(sha) || git(&self.worktree, &["rev-parse", "HEAD"])? != sha {
            return Err("candidate revision changed; evidence stale".into());
        }
        Ok(())
    }
    fn missing(&self) -> Result<bool, String> {
        match fs::symlink_metadata(&self.worktree) {
            Ok(_) => Ok(false),
            Err(e) if e.kind() == ErrorKind::NotFound => Ok(true),
            Err(_) => Err("worktree unavailable".into()),
        }
    }
    fn prepare(&self) -> Result<(), String> {
        if self.missing()? {
            git(
                &self.project,
                &[
                    "worktree",
                    "add",
                    "-b",
                    &self.branch,
                    "--",
                    as_str(&self.worktree)?,
                    &self.base,
                ],
            )?;
        }
        self.inspect()?;
        if git(&self.worktree, &["rev-parse", "HEAD"])? != self.base {
            return Err("worktree has diverged from the recorded base".into());
        }
        Ok(())
    }
    fn cleanup(&self, sha: &str) -> Result<(), String> {
        if self.missing()? {
            let listing = git(&self.project, &["worktree", "list", "--porcelain", "-z"])?;
            if listing
                .split('\0')
                .any(|line| line.strip_prefix("worktree ") == self.worktree.to_str())
            {
                return Err(
                    "worktree directory missing but Git registration remains; inspect locally"
                        .into(),
                );
            }
            return Ok(());
        }
        self.inspect_at(sha)?;
        ensure_removable(&self.worktree)?;
        git(
            &self.project,
            &["worktree", "remove", "--", as_str(&self.worktree)?],
        )?;
        Ok(())
    }
}

#[derive(Deserialize)]
#[serde(tag = "op", rename_all = "snake_case", deny_unknown_fields)]
enum Operation {
    Origin {
        project: PathBuf,
        repository: String,
    },
    Prepare {
        attempt: AttemptLocation,
    },
    Inspect {
        attempt: AttemptLocation,
    },
    Clean {
        attempt: AttemptLocation,
        sha: String,
    },
    Cleanup {
        attempt: AttemptLocation,
        sha: String,
    },
}

pub fn run(mut request: Value) -> Result<Value, String> {
    if request
        .as_object_mut()
        .and_then(|o| o.remove("version"))
        .and_then(|v| v.as_u64())
        != Some(1)
    {
        return Err("unsupported worktree protocol version".into());
    }
    let op: Operation = serde_json::from_value(request).map_err(|_| "invalid worktree request")?;
    match op {
        Operation::Origin {
            project,
            repository,
        } => check_origin(&project, &repository)?,
        Operation::Prepare { attempt } => attempt.checked()?.prepare()?,
        Operation::Inspect { attempt } => attempt.checked()?.inspect()?,
        Operation::Clean { attempt, sha } => {
            attempt.checked()?.inspect_at(&sha)?;
            check_clean(&attempt.worktree)?;
        }
        Operation::Cleanup { attempt, sha } => attempt.checked()?.cleanup(&sha)?,
    };
    Ok(Value::Null)
}
