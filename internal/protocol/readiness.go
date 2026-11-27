// SPDX-License-Identifier: MPL-2.0
package protocol

type ProjectReadinessRequest struct {
	Protocol int    `json:"protocol"`
	Project  string `json:"projectId"`
}

type HostedProjectReadiness struct {
	Repository             string   `json:"repository"`
	GitHubConnected        bool     `json:"githubConnected"`
	DeploymentEnvironments []string `json:"deploymentEnvironments"`
}
