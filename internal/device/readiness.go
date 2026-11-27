// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func ProjectReadiness(ctx context.Context, project, environment string, runValidator bool, out io.Writer) error {
	p, err := getProfile()
	if err != nil || p.Enrollment.DeviceID == "" {
		return errors.New("Pair this computer with bugsnitch pair, then assign it to the project in the workspace.")
	}
	checks := make([]runner.ReadinessCheck, 0, 4)
	var hosted protocol.HostedProjectReadiness
	err = send(ctx, p, "project-readiness", protocol.ProjectReadinessRequest{Protocol: protocol.Version, Project: project}, &hosted)
	if err != nil {
		message := "Check the paired service connection and install the matching Bugsnitch server and runner."
		if errors.Is(err, statusError(403)) {
			message = "Assign this computer to the project in the workspace or renew its pairing."
		}
		checks = append(checks, runner.ReadinessCheck{Name: "Workspace", Message: message})
	}
	if hosted.Repository != "" {
		message := "Connect the project repository to the GitHub App in Settings."
		if hosted.GitHubConnected {
			message = hosted.Repository + " is connected."
		}
		checks = append(checks, runner.ReadinessCheck{Name: "GitHub", Ready: hosted.GitHubConnected, Message: message})
		configured := len(hosted.DeploymentEnvironments) > 0
		if environment != "" {
			configured = slices.Contains(hosted.DeploymentEnvironments, environment)
		}
		message = "Open an incident's Environment verification panel and connect a GitHub deployment monitor for the target environment."
		if configured {
			message = "Active monitor environments: " + strings.Join(hosted.DeploymentEnvironments, ", ") + "."
		}
		checks = append(checks, runner.ReadinessCheck{Name: "Deployment verification", Ready: configured, Message: message})
	}
	path, err := journalContext(ctx, "SELECT path FROM mappings WHERE id="+sqlValue(project)+";")
	if err != nil || path == "" {
		checks = append(checks, runner.ReadinessCheck{Name: "Checkout", Message: fmt.Sprintf("Run bugsnitch register --project %s --path /absolute/path/to/checkout.", project)})
	} else {
		repo := hosted.Repository
		if repo == "" && !runValidator {
			for _, assigned := range p.Enrollment.Projects {
				if assigned.ID == project {
					repo = assigned.Repository
					break
				}
			}
		}
		if repo != "" {
			checks = append(checks, runner.CheckProjectReadiness(ctx, p.Enrollment.Workspace, repo, path, runValidator)...)
		}
	}
	ready := true
	for _, check := range checks {
		status := "OK"
		if !check.Ready {
			status, ready = "ACTION", false
		}
		fmt.Fprintf(out, "%s  %s  %s\n", status, check.Name, check.Message)
	}
	if !ready {
		return errors.New("project readiness checks need attention")
	}
	return nil
}
