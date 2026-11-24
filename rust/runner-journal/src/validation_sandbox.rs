// SPDX-License-Identifier: MPL-2.0
//! A read-only worktree and writable temporary storage for explicitly isolated
//! validators. No network, provider credentials or shared Git directory.
use std::{io, path::Path, process::Command};

#[cfg(target_os = "macos")]
pub fn command(program: &Path, tree: &Path, scratch: &Path) -> io::Result<Command> {
    const PROFILE: &str = r#"
(version 1)
(deny default)
(import "system.sb")
(allow file-read-metadata (path-ancestors (param "TREE")) (path-ancestors (param "SCRATCH")))
(allow file-read* (literal "/private/var/select/sh"))
(allow process-exec process-fork sysctl-read)
(allow signal (target self))
(allow file-read* file-map-executable
    (subpath "/System") (subpath "/usr/lib") (subpath "/bin") (subpath "/usr/bin")
    (literal "/dev/null") (literal "/dev/random") (literal "/dev/urandom")
    (literal (param "PROGRAM")) (subpath (param "TREE")) (subpath (param "SCRATCH")))
(allow file-write* (literal "/dev/null") (subpath (param "SCRATCH")))
"#;
    let mut cmd = Command::new("/usr/bin/sandbox-exec");
    for (name, path) in [("PROGRAM", program), ("TREE", tree), ("SCRATCH", scratch)] {
        let mut param = std::ffi::OsString::from(format!("{name}="));
        param.push(path.canonicalize()?);
        cmd.arg("-D").arg(param);
    }
    cmd.arg("-p").arg(PROFILE).arg(program);
    Ok(cmd)
}

#[cfg(target_os = "linux")]
pub fn command(program: &Path, tree: &Path, _scratch: &Path) -> io::Result<Command> {
    let mut cmd = Command::new("/usr/bin/bwrap");
    cmd.args([
        "--die-with-parent",
        "--unshare-all",
        "--new-session",
        "--cap-drop",
        "ALL",
        "--clearenv",
        "--setenv",
        "PATH",
        "/usr/bin:/bin",
        "--setenv",
        "LANG",
        "C",
        "--setenv",
        "HOME",
        "/tmp",
        "--setenv",
        "TMPDIR",
        "/tmp",
        "--proc",
        "/proc",
        "--dev",
        "/dev",
        "--tmpfs",
        "/tmp",
    ]);
    for dir in [
        "/usr/bin",
        "/usr/lib",
        "/usr/lib64",
        "/usr/share",
        "/bin",
        "/lib",
        "/lib64",
    ] {
        if Path::new(dir).exists() {
            cmd.args(["--ro-bind", dir, dir]);
        }
    }
    cmd.arg("--ro-bind")
        .arg(tree)
        .args(["/work", "--ro-bind"])
        .arg(program)
        .args([
            "/run/bugsnitch-validator",
            "--chdir",
            "/work",
            "--",
            "/run/bugsnitch-validator",
        ]);
    Ok(cmd)
}
