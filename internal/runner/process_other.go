//go:build !darwin && !linux

// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"errors"
	"os"
	"os/exec"
)

func isolateSupervisor(cmd *exec.Cmd) {}

func lockFile(f *os.File) error {
	return errors.New("execution currently requires macOS or Linux advisory locks")
}
