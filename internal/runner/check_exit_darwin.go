// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func checkGroupIsGone(pid int, err error) bool {
	// Darwin may report EPERM when the entire group consists of zombies.
	// A live member still needs to be treated as a failed termination.
	if !errors.Is(err, syscall.EPERM) {
		return false
	}
	group, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
	if err != nil {
		return false
	}
	const zombie = 5 // Darwin <sys/proc.h> SZOMB
	for _, process := range group {
		if process.Proc.P_stat != zombie {
			return false
		}
	}
	return true
}

func observeCheckExit(pid int) error {
	fd, err := syscall.Kqueue()
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	change := syscall.Kevent_t{Ident: uint64(pid), Filter: syscall.EVFILT_PROC, Flags: syscall.EV_ADD | syscall.EV_ONESHOT, Fflags: syscall.NOTE_EXIT}
	events := make([]syscall.Kevent_t, 1)
	for {
		n, err := syscall.Kevent(fd, []syscall.Kevent_t{change}, events, nil)
		if err == syscall.EINTR || n == 0 && err == nil {
			continue
		}
		if err == nil && events[0].Flags&syscall.EV_ERROR != 0 {
			err = syscall.Errno(events[0].Data)
		}
		// A short-lived child may have exited before registration. We still
		// own its unreaped PID and can safely signal its process group.
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
}
