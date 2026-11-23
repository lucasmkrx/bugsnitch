//go:build darwin || linux

// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Terminal signals sent to the runner must leave the supervisor alive to
// observe the closed control pipe and stop its separately grouped validator.
func isolateSupervisor(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Keep the group leader unreaped until the group has been signalled. The
// kernel cannot reuse its PID during this interval, even if it exits early.
func runCheckGroup(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	isolateSupervisor(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	ended := make(chan struct{})
	var observeErr error
	go func() {
		observeErr = observeCheckExit(pid)
		close(ended)
	}()
	select {
	case <-ended:
	case <-ctx.Done():
	}
	// Also stop any ordinary descendants left by a launcher that has exited.
	stopErr := stopCheckGroup(pid)
	if errors.Is(stopErr, syscall.ESRCH) {
		stopErr = nil
	} else if stopErr != nil {
		_ = cmd.Process.Kill()
	}
	<-ended
	return errors.Join(cmd.Wait(), observeErr, stopErr)
}

func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
