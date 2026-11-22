//go:build darwin || linux

// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"os"
	"os/exec"
	"syscall"
)

// Terminal signals sent to the runner must leave the supervisor alive to
// observe the closed control pipe and stop its separately grouped validator.
func isolateSupervisor(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
