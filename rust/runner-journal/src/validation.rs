// SPDX-License-Identifier: MPL-2.0
//! A single local validation. The parent keeps stdin open after the launch frame.
//! EOF cancels the whole group, including when the parent exits without cleanup.

#[cfg(not(any(target_os = "macos", target_os = "linux")))]
pub fn run() -> std::process::ExitCode {
    // These targets also lack the runner's attempt ownership lock.
    std::process::ExitCode::FAILURE
}

#[cfg(any(target_os = "macos", target_os = "linux"))]
pub use unix::run;

#[cfg(any(target_os = "macos", target_os = "linux"))]
mod unix {
    use crate::{
        integration::{self, IntegrationValidation},
        worktree::AttemptLocation,
    };
    use serde::Deserialize;
    use std::{
        env,
        fs::{self, File},
        io::{self, Read, Write},
        os::{
            fd::{AsFd, AsRawFd},
            unix::{ffi::OsStrExt, fs::MetadataExt, process::CommandExt},
        },
        path::{Path, PathBuf},
        process::{Child, Command, ExitCode, Stdio},
        time::{Duration, Instant},
    };

    const VERSION: u8 = 1;
    const MAX_FRAME: usize = 8 << 20;
    const POLL_INTERVAL: Duration = Duration::from_millis(25);
    const STOP_DEADLINE: Duration = Duration::from_secs(5);

    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Launch {
        version: u8,
        #[serde(default)]
        sandbox: String,
        worktree: PathBuf,
        project: PathBuf,
        argv: Vec<String>,
        timeout_seconds: u64,
        home: PathBuf,
        repository: String,
        attempt: Option<AttemptLocation>,
        candidate_sha: Option<String>,
        integration: Option<IntegrationValidation>,
    }

    #[derive(Clone, Copy)]
    #[repr(u8)]
    enum Outcome {
        Exited,
        Cancelled,
        TimedOut,
        DidNotStart,
        InvalidRequest,
        ChangedWorktree,
    }

    struct Scratch(PathBuf);

    impl Scratch {
        fn new() -> io::Result<Self> {
            let template = env::temp_dir().join("bugsnitch-check-XXXXXX");
            let mut bytes = template.as_os_str().as_bytes().to_vec();
            bytes.push(0);
            // mkdtemp atomically creates a private 0700 directory and replaces
            // the trailing Xs in the writable, NUL-terminated path buffer.
            if unsafe { libc::mkdtemp(bytes.as_mut_ptr().cast()) }.is_null() {
                return Err(io::Error::last_os_error());
            }
            bytes.pop();
            Ok(Self(Path::new(std::ffi::OsStr::from_bytes(&bytes)).into()))
        }
    }

    impl Drop for Scratch {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    fn cancellation(input: &File, duration: Duration) -> io::Result<bool> {
        let mut fd = libc::pollfd {
            fd: input.as_raw_fd(),
            events: libc::POLLIN,
            revents: 0,
        };
        // Only the initial request carries data. HUP, a byte, or a pipe error
        // after that request ends authority to run this validator.
        let rc = unsafe { libc::poll(&mut fd, 1, duration.as_millis() as i32) };
        if rc < 0 {
            let e = io::Error::last_os_error();
            if e.kind() != io::ErrorKind::Interrupted {
                return Err(e);
            }
        }
        Ok(rc > 0)
    }

    fn finished(child: &Child) -> io::Result<bool> {
        // Keep the group leader waitable until signaling is complete. This
        // pins its PID so it cannot be recycled as another process group.
        let mut info: libc::siginfo_t = unsafe { std::mem::zeroed() };
        let rc = unsafe {
            libc::waitid(
                libc::P_PID,
                child.id(),
                &mut info,
                libc::WEXITED | libc::WNOHANG | libc::WNOWAIT,
            )
        };
        if rc < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(unsafe { info.si_pid() } != 0)
    }

    fn wait_tick(deadline: Instant) -> io::Result<()> {
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            return Err(io::ErrorKind::TimedOut.into());
        }
        std::thread::sleep(remaining.min(POLL_INTERVAL));
        Ok(())
    }

    fn reap_leader(child: &mut Child, deadline: Instant) -> io::Result<i32> {
        loop {
            match child.try_wait() {
                Ok(Some(status)) => return Ok(status.code().unwrap_or(-1)),
                Ok(None) => wait_tick(deadline)?,
                Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                Err(e) => return Err(e),
            }
        }
    }

    #[cfg(target_os = "linux")]
    fn adopt_descendants() -> io::Result<()> {
        // This executable supervises exactly one validator. When a group member
        // exits, its orphaned descendants become our children, even in containers
        // whose PID 1 does not reap orphans.
        let rc = unsafe {
            libc::prctl(
                libc::PR_SET_CHILD_SUBREAPER,
                1 as libc::c_ulong,
                0 as libc::c_ulong,
                0 as libc::c_ulong,
                0 as libc::c_ulong,
            )
        };
        if rc < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    #[cfg(target_os = "linux")]
    fn reap_descendants(group: libc::pid_t, deadline: Instant) -> io::Result<()> {
        loop {
            // waitpid can only reap this supervisor's own children. The original
            // leader has been reaped, so no terminating signal can be sent here.
            let pid = unsafe { libc::waitpid(-group, std::ptr::null_mut(), libc::WNOHANG) };
            if pid > 0 {
                continue;
            }
            if pid < 0 {
                let e = io::Error::last_os_error();
                if e.kind() == io::ErrorKind::Interrupted {
                    continue;
                }
                if e.raw_os_error() != Some(libc::ECHILD) {
                    return Err(e);
                }
                // Confirm absence across any concurrent reparenting. Signal 0
                // probes group existence without delivering a signal. Numeric
                // reuse can only delay or withhold a terminal receipt.
                if unsafe { libc::kill(-group, 0) } < 0 {
                    let e = io::Error::last_os_error();
                    if e.raw_os_error() == Some(libc::ESRCH) {
                        return Ok(());
                    }
                    return Err(e);
                }
            }
            wait_tick(deadline)?;
        }
    }

    fn supervise(mut child: Child, input: &File, deadline: Instant) -> io::Result<(Outcome, i32)> {
        let outcome = loop {
            match finished(&child) {
                Ok(true) => break Ok(Outcome::Exited),
                Ok(false) => {}
                Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                Err(e) if e.raw_os_error() == Some(libc::ECHILD) => return Err(e),
                Err(e) => break Err(e),
            }
            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                break Ok(Outcome::TimedOut);
            }
            match cancellation(input, remaining.min(POLL_INTERVAL)) {
                Ok(true) => break Ok(Outcome::Cancelled),
                Ok(false) => {}
                Err(e) => break Err(e),
            }
        };
        // Kill remaining group members even on ordinary leader exit. A validator
        // deliberately escaping this process group is outside the trusted profile.
        let group = child.id() as libc::pid_t;
        let stop_deadline = Instant::now() + STOP_DEADLINE;
        let rc = unsafe { libc::kill(-group, libc::SIGKILL) };
        if rc < 0 {
            let err = io::Error::last_os_error();
            // Darwin can return EPERM when the only group member is a zombie.
            // A waitable leader still supplies its actual terminal status.
            let exited_leader = err.raw_os_error() == Some(libc::EPERM) && finished(&child)?;
            if err.raw_os_error() != Some(libc::ESRCH) && !exited_leader {
                // Try to reap the direct child even if group signaling failed.
                let _ = child.kill();
                let _ = reap_leader(&mut child, stop_deadline);
                return Err(err);
            }
        }
        #[cfg(target_os = "macos")]
        let group_result = crate::macos_group::wait_for_descendants(group);
        let code = reap_leader(&mut child, stop_deadline)?;
        #[cfg(target_os = "macos")]
        group_result?;
        #[cfg(target_os = "linux")]
        reap_descendants(group, stop_deadline)?;
        Ok((outcome?, code))
    }

    fn launch(request: Launch, input: &File) -> io::Result<(Outcome, i32)> {
        if request.version != VERSION
            || !(1..=48).contains(&request.argv.len())
            || !(1..=14400).contains(&request.timeout_seconds)
            || !request.project.is_absolute()
            || !request.worktree.is_absolute()
            || !Path::new(&request.argv[0]).is_absolute()
        {
            return Ok((Outcome::InvalidRequest, -1));
        }
        let (Ok(project), Ok(tree), Ok(program)) = (
            fs::canonicalize(&request.project),
            fs::canonicalize(&request.worktree),
            fs::canonicalize(&request.argv[0]),
        ) else {
            return Ok((Outcome::DidNotStart, -1));
        };
        if tree == project
            || program.starts_with(&project)
            || program.starts_with(&tree)
            || !fs::symlink_metadata(tree.join(".git")).is_ok_and(|meta| meta.is_file())
        {
            return Ok((Outcome::DidNotStart, -1));
        }
        let Ok(scratch) = Scratch::new() else {
            return Ok((Outcome::DidNotStart, -1));
        };
        let Ok(directory) = File::open(&tree) else {
            return Ok((Outcome::DidNotStart, -1));
        };
        let identity = match (
            &request.attempt,
            &request.candidate_sha,
            &request.integration,
        ) {
            (Some(attempt), Some(sha), None) => attempt.verify_launch(&request.worktree, sha),
            (None, None, Some(receipt)) => integration::verify_launch(
                &request.home,
                &request.project,
                &request.repository,
                &request.worktree,
                receipt,
            ),
            _ => return Ok((Outcome::InvalidRequest, -1)),
        };
        let same_directory = directory.metadata().and_then(|open| {
            fs::metadata(&tree)
                .map(|current| (current.dev(), current.ino()) == (open.dev(), open.ino()))
        });
        if identity.is_err() || !same_directory.unwrap_or(false) {
            return Ok((Outcome::ChangedWorktree, -1));
        }
        if cancellation(input, Duration::ZERO)? {
            return Ok((Outcome::DidNotStart, -1));
        }
        let deadline = Instant::now() + Duration::from_secs(request.timeout_seconds);
        let mut command = match request.sandbox.as_str() {
            "" => Command::new(&program),
            "isolated" => match crate::validation_sandbox::command(&program, &tree, &scratch.0) {
                Ok(command) => command,
                Err(_) => return Ok((Outcome::DidNotStart, -1)),
            },
            _ => return Ok((Outcome::InvalidRequest, -1)),
        };
        command
            .args(&request.argv[1..])
            .env_clear()
            .env(
                "PATH",
                if request.sandbox.is_empty() {
                    env::var_os("PATH").unwrap_or_default()
                } else {
                    "/usr/bin:/bin".into()
                },
            )
            .env("HOME", &scratch.0)
            .env("TMPDIR", &scratch.0)
            .env("LANG", "C")
            .env("GIT_TERMINAL_PROMPT", "0")
            .env("GIT_CONFIG_GLOBAL", "/dev/null")
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .process_group(0);
        // Bind the launch to the inspected directory inode across a rename.
        // fchdir is async-signal-safe and File's close-on-exec flag is preserved.
        unsafe {
            command.pre_exec(move || {
                if libc::fchdir(directory.as_raw_fd()) == 0 {
                    Ok(())
                } else {
                    Err(io::Error::last_os_error())
                }
            });
        }
        #[cfg(target_os = "linux")]
        if adopt_descendants().is_err() {
            return Ok((Outcome::DidNotStart, -1));
        }
        match command.spawn() {
            Ok(child) => supervise(child, input, deadline),
            Err(_) => Ok((Outcome::DidNotStart, -1)),
        }
    }

    fn serve() -> io::Result<()> {
        // Use an unbuffered descriptor. Any control bytes following the frame
        // must stay visible to poll, even when delivered in the same write.
        let mut input = File::from(io::stdin().as_fd().try_clone_to_owned()?);
        let mut header = [0; 4];
        input.read_exact(&mut header)?;
        let length = u32::from_be_bytes(header) as usize;
        if length > MAX_FRAME {
            return Err(io::ErrorKind::InvalidData.into());
        }
        let mut data = vec![0; length];
        input.read_exact(&mut data)?;
        let (outcome, code) = match serde_json::from_slice(&data) {
            Ok(request) => launch(request, &input)?,
            Err(_) => (Outcome::InvalidRequest, -1),
        };
        let mut receipt = [0u8; 6];
        receipt[..2].copy_from_slice(&[VERSION, outcome as u8]);
        receipt[2..].copy_from_slice(&code.to_be_bytes());
        io::stdout().lock().write_all(&receipt)
    }

    pub fn run() -> ExitCode {
        match serve() {
            Ok(()) => ExitCode::SUCCESS,
            Err(_) => ExitCode::FAILURE,
        }
    }
}
