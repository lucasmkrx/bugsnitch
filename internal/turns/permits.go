// SPDX-License-Identifier: MPL-2.0
// Package turns defines the wire contract for owner-initiated model permits.
package turns

import "regexp"

type Operation struct {
	Protocol  int    `json:"protocol"`
	Workspace string `json:"workspaceId"`
	Device    string `json:"deviceId"`
	ID        string `json:"permitId"`
	Role      string `json:"role"`
	Project   string `json:"projectId,omitempty"`
	Version   int    `json:"version"`
}
type Receipt struct {
	ID      string `json:"permitId"`
	Version int    `json:"version"`
}

var identity = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (a Operation) Valid() bool {
	if !identity.MatchString(a.ID) || !identity.MatchString(a.Device) || a.Workspace == "" || len(a.Workspace) > 100 || (a.Version != 1 && a.Version != 2) {
		return false
	}
	switch a.Role {
	case "manager":
		return identity.MatchString(a.Project)
	case "planner", "proof":
		return a.Project == ""
	default:
		return false
	}
}
