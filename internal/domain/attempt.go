// SPDX-License-Identifier: MPL-2.0
package domain

// ValidAttempt describes a runner snapshot. Intermediate versions may be
// absent after a disconnect. Revisions can return review to implementation.
func ValidAttempt(phase, state, candidate string) bool {
	switch state {
	case "completed":
		return phase == "wrap_up" && candidate != ""
	case "waiting", "running", "failed", "cancelled":
	default:
		return false
	}
	switch phase {
	case "plan":
		return candidate == ""
	case "implement":
		return true
	case "validate", "review", "wrap_up":
		return candidate != ""
	}
	return false
}
