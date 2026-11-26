// SPDX-License-Identifier: MPL-2.0
package protocol

import (
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
)

// WorkerStart binds a portal request to one device, checkout, GitHub revision and policy.
type WorkerStart struct {
	ID       string        `json:"id"`
	Device   string        `json:"deviceId"`
	Project  string        `json:"projectId"`
	Issue    int           `json:"issueNumber"`
	Plan     string        `json:"planId"`
	Policy   int64         `json:"policyVersion"`
	Status   string        `json:"status"`
	Attempt  string        `json:"attemptId,omitempty"`
	Reason   string        `json:"reason,omitempty"`
	Title    string        `json:"title"`
	Revision IssueRevision `json:"revision"`
	Expires  time.Time     `json:"expiresAt"`
}

type WorkerStartPoll struct {
	Protocol  int          `json:"protocol"`
	Project   string       `json:"projectId"`
	ID        string       `json:"id,omitempty"`
	Operation string       `json:"operation"`
	Proposal  *domain.Plan `json:"proposal,omitempty"`
	Reason    string       `json:"reason,omitempty"`
}
