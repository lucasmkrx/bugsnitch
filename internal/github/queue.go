// SPDX-License-Identifier: MPL-2.0
package github

// RuleSnapshot describes active rulesets observed for one named target branch.
// A missing snapshot represents unknown access, incomplete pagination or unsupported data.
type RuleSnapshot struct {
	Digest         string `json:"digest"`
	QueueRequired  bool   `json:"queueRequired"`
	RequiredChecks int    `json:"requiredChecks"`
	AppBoundChecks int    `json:"appBoundChecks"`
}
type QueueObservation struct {
	OwnerType  string        `json:"ownerType"`
	Present    bool          `json:"present"`
	EntryState string        `json:"entryState,omitempty"`
	Position   *int          `json:"position,omitempty"`
	EntryHead  string        `json:"entryHead,omitempty"`
	EntryBase  string        `json:"entryBase,omitempty"`
	Rules      *RuleSnapshot `json:"rules,omitempty"`
}
