//go:build !darwin && !linux

// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func isolateSupervisor(cmd *exec.Cmd) {}

func runCheckGroup(context.Context, *exec.Cmd) error {
	return errors.New("verification checks require macOS or Linux process groups")
}

func lockFile(f *os.File) error {
	return errors.New("execution currently requires macOS or Linux advisory locks")
}
