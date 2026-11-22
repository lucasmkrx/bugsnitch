// SPDX-License-Identifier: MPL-2.0
use std::{
    io,
    mem::{MaybeUninit, size_of},
    thread,
    time::{Duration, Instant},
};

/// Darwin's <sys/proc_info.h> selector restricts the kernel query to one group.
const PROC_PGRP_ONLY: u32 = 2;

fn members(group: libc::pid_t) -> io::Result<Vec<libc::pid_t>> {
    let mut pids = vec![0; 32];
    loop {
        let capacity = pids.len() * size_of::<libc::pid_t>();
        // SAFETY: pids has capacity initialized for the full byte count passed.
        let bytes = unsafe {
            libc::proc_listpids(
                PROC_PGRP_ONLY,
                group as u32,
                pids.as_mut_ptr().cast(),
                capacity as libc::c_int,
            )
        };
        if bytes <= 0 {
            return Err(io::Error::other("cannot enumerate validator process group"));
        }
        let bytes = bytes as usize;
        if bytes > capacity || !bytes.is_multiple_of(size_of::<libc::pid_t>()) {
            return Err(io::Error::other("incomplete validator process group list"));
        }
        if bytes < capacity {
            pids.truncate(bytes / size_of::<libc::pid_t>());
            if !pids.contains(&group) || pids.iter().any(|&pid| pid <= 0) {
                return Err(io::Error::other("lost sight of the pinned validator group"));
            }
            pids.sort_unstable();
            return Ok(pids);
        }
        // A full buffer may be truncated. Never make a stop claim from it.
        if pids.len() >= 65_536 {
            return Err(io::Error::other(
                "validator process group exceeds inspection limit",
            ));
        }
        pids.resize(pids.len() * 2, 0);
    }
}

fn exited_or_gone(group: libc::pid_t, pid: libc::pid_t) -> io::Result<bool> {
    let mut info = MaybeUninit::<libc::proc_bsdshortinfo>::uninit();
    let size = size_of::<libc::proc_bsdshortinfo>() as libc::c_int;
    // XNU accepts a nonzero arg to include zombie processes for SHORTBSDINFO.
    // SAFETY: the exact structure size is checked before reading the buffer.
    let bytes = unsafe {
        libc::proc_pidinfo(
            pid,
            libc::PROC_PIDT_SHORTBSDINFO,
            1,
            info.as_mut_ptr().cast(),
            size,
        )
    };
    if bytes != size {
        if bytes == 0
            && io::Error::last_os_error().raw_os_error() == Some(libc::ESRCH)
            && pid != group
        {
            return Ok(true);
        }
        return Err(io::Error::other(
            "cannot confirm validator group member state",
        ));
    }
    // SAFETY: libproc initialized a full short BSD info record above.
    let info = unsafe { info.assume_init() };
    if info.pbsi_pid != pid as u32 || (pid == group && info.pbsi_pgid != group as u32) {
        return Err(io::Error::other("validator process group identity changed"));
    }
    Ok(info.pbsi_pgid != group as u32 || info.pbsi_status == libc::SZOMB)
}

/// The supervisor has signalled this group and owns its unreaped leader. A
/// zombie holds no running code. Keep the leader pinned through both snapshots
/// so its PGID cannot be recycled while we confirm membership.
pub(crate) fn wait_for_descendants(group: libc::pid_t) -> io::Result<()> {
    if group <= 0 {
        return Err(io::Error::other("invalid validator process group"));
    }
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        let pids = members(group)?;
        let mut finished = true;
        for &pid in &pids {
            if Instant::now() >= deadline {
                return Err(io::ErrorKind::TimedOut.into());
            }
            if !exited_or_gone(group, pid)? {
                finished = false;
                break;
            }
        }
        // A member could have forked just before termination during the first
        // snapshot. Require the same membership after checking every state.
        if finished && pids == members(group)? {
            return Ok(());
        }
        if Instant::now() >= deadline {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "validator group termination unconfirmed",
            ));
        }
        thread::sleep(Duration::from_millis(10));
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        os::unix::process::CommandExt,
        process::{Child, Command},
        sync::mpsc,
    };

    struct Owned(Child);
    impl Drop for Owned {
        fn drop(&mut self) {
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }

    #[test]
    fn pins_group_until_live_member_has_exited() {
        let mut leader = Owned(
            Command::new("/bin/sleep")
                .arg("10")
                .process_group(0)
                .spawn()
                .unwrap(),
        );
        let group = leader.0.id() as libc::pid_t;
        let mut member = Owned(
            Command::new("/bin/sleep")
                .arg("10")
                .process_group(group)
                .spawn()
                .unwrap(),
        );
        leader.0.kill().unwrap();
        let (send, receive) = mpsc::channel();
        thread::scope(|scope| {
            scope.spawn(move || {
                let _ = send.send(wait_for_descendants(group));
            });
            assert!(matches!(
                receive.recv_timeout(Duration::from_millis(100)),
                Err(mpsc::RecvTimeoutError::Timeout)
            ));
            member.0.kill().unwrap();
            receive
                .recv_timeout(Duration::from_secs(3))
                .unwrap()
                .unwrap();
        });
        // The inspection itself must not reap either process.
        assert!(member.0.wait().is_ok());
        assert!(leader.0.wait().is_ok());
    }
}
