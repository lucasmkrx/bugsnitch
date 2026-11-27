// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type ReadinessCheck struct {
	Name    string
	Ready   bool
	Message string
}

// CheckProjectReadiness checks the same checkout, validator and sandbox used
// by attempt execution. The optional smoke run uses a disposable export.
func CheckProjectReadiness(ctx context.Context, workspace, repo, checkout string, runValidator bool) []ReadinessCheck {
	if err := checkOrigin(checkout, repo); err != nil {
		return []ReadinessCheck{{"Checkout", false, "Check Git and the native runner bundle, then register the canonical checkout for " + repo + ": " + err.Error()}}
	}
	checks := []ReadinessCheck{{"Checkout", true, "Registered checkout matches " + repo + "."}}
	p, err := project(workspace, repo)
	if err != nil || p.Path != checkout || len(p.Config.Validate) == 0 {
		return append(checks, ReadinessCheck{"Validator", false, "Run bugsnitch project-add for this workspace and repository with its reviewed validation command."})
	}
	program, err := trustedProjectTool(checkout, p.Config.Validate[0])
	if err != nil || program != p.Config.Validate[0] {
		return append(checks, ReadinessCheck{"Validator", false, "Install the configured validator outside the checkout and run bugsnitch project-add again."})
	}
	if p.Config.ValidationSandbox == "isolated" {
		launcher := "/usr/bin/bwrap"
		if runtime.GOOS == "darwin" {
			launcher = "/usr/bin/sandbox-exec"
		}
		if info, err := os.Stat(launcher); err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return append(checks, ReadinessCheck{"Validator", false, "Install the isolated validation launcher at " + launcher + "."})
		}
	}
	if !runValidator {
		return append(checks, ReadinessCheck{"Validator", true, "The configured executable and required launcher are installed. Use --run-validator to run the command on a disposable export."})
	}
	revision, err := probeProjectValidator(ctx, p)
	if err != nil {
		return append(checks, ReadinessCheck{"Validator", false, "Check the validator's dependencies and command in the project configuration: " + err.Error()})
	}
	return append(checks, ReadinessCheck{"Validator", true, "The configured validator passed on the exported commit " + revision + "."})
}

func probeProjectValidator(ctx context.Context, p Project) (string, error) {
	revision, err := git(p.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("the checkout needs a committed HEAD: %w", err)
	}
	dir, err := os.MkdirTemp("", "bugsnitch-readiness-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		return "", err
	}
	if err := exportRevision(ctx, p.Path, source, revision); err != nil {
		return "", err
	}
	secs := p.Config.ValidationTimeoutSeconds
	if secs == 0 {
		secs = 600
	}
	_, err = runNativeValidation(ctx, validationLaunch{Version: 1, Readiness: true,
		Worktree: source, Project: p.Path, Repository: p.Repository, Argv: p.Config.Validate,
		Sandbox: p.Config.ValidationSandbox, TimeoutSeconds: secs})
	return revision, err
}
