// SPDX-License-Identifier: MPL-2.0
package protocol

type NextRequest struct {
	Protocol    int    `json:"protocol"`
	Project     string `json:"projectId"`
	WaitForTurn bool   `json:"waitForTurn,omitempty"`
	Issues      []int  `json:"issues,omitempty"`
}

// IssueRevision identifies the exact canonical issue observed in an approved plan.
type IssueRevision struct {
	GitHubID int64  `json:"githubId"`
	Digest   string `json:"requirementsDigest"`
	Revision string `json:"updatedAt"`
}

// IssueSelection is the versioned document accepted by the local-start CLI.
type IssueSelection struct {
	GitHubID           int64    `json:"githubId,omitempty"`
	RequirementsDigest string   `json:"requirementsDigest,omitempty"`
	Schema             int      `json:"schema"`
	Repository         string   `json:"repository"`
	Number             int      `json:"number"`
	URL                string   `json:"url"`
	Title              string   `json:"title"`
	State              string   `json:"state"`
	UpdatedAt          string   `json:"updatedAt"`
	Criteria           []string `json:"criteria"`
}

// RunScope identifies the enrolled device and the approved scope observed by next.
type RunScope struct {
	Device  string `json:"deviceId"`
	Project string `json:"projectId"`
	Plan    string `json:"planId"`
	Policy  int64  `json:"policyVersion"`
}
type Recommendation struct {
	Scope     *RunScope             `json:"scope,omitempty"`
	Selection *IssueSelection       `json:"selection,omitempty"`
	Reason    string                `json:"reason,omitempty"`
	Bindings  map[int]IssueRevision `json:"bindings,omitempty"`
	Hold      bool                  `json:"hold,omitempty"`
}
