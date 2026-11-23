// SPDX-License-Identifier: MPL-2.0
package runner

import "golang.org/x/sys/unix"

func checkGroupIsGone(int, error) bool { return false }

func observeCheckExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err
		}
	}
}
