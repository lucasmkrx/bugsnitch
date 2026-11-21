// SPDX-License-Identifier: MPL-2.0
package protocol

import "slices"

const WorkerLeaseSeconds = 15

type WorkerLease struct {
	Scope          *RunScope `json:"scope,omitempty"`
	Protocol       int       `json:"protocol"`
	Workspace      string    `json:"workspaceId"`
	Device         string    `json:"deviceId"`
	Project        string    `json:"projectId"`
	Attempt        string    `json:"attemptId"`
	Base           string    `json:"baseSha"`
	Issue          int       `json:"issueNumber"`
	Sequence       int64     `json:"sequence"`
	SubjectVersion int       `json:"subjectVersion"`
	Turn           bool      `json:"modelTurn"`
}
type WorkerGrant struct {
	Sequence     int64    `json:"sequence"`
	ValidSeconds int      `json:"validSeconds"`
	Control      *Control `json:"control,omitempty"`
}

func ValidReason(s string) bool {
	return slices.Contains([]string{"", "sign_in_required", "allowance_exhausted", "model_unavailable", "provider_rate_limited", "provider_unavailable", "device_unauthorized", "authority_unreachable", "authority_stale", "control_pending", "local_capacity", "task_failed"}, s)
}
