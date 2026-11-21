// SPDX-License-Identifier: MPL-2.0
package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
)

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

func (r Repository) branchRules(ctx context.Context, target string) *RuleSnapshot {
	if !ValidBranch(target) {
		return nil
	}
	var definitions []string
	observed := RuleSnapshot{}
	for page := 1; page <= 5; page++ {
		route := r.path + "/rules/branches/" + url.PathEscape(target) + "?per_page=100&page=" + strconv.Itoa(page)
		var rules []json.RawMessage
		if err := r.app.request(ctx, r.token, "GET", route, nil, &rules, 200); err != nil || rules == nil {
			return nil
		}
		for _, raw := range rules {
			var rule struct {
				Type       string
				Parameters struct {
					Checks []struct {
						Context string
						AppID   *int64 `json:"integration_id"`
					} `json:"required_status_checks"`
				}
			}
			if json.Unmarshal(raw, &rule) != nil || rule.Type == "" {
				return nil
			}
			if rule.Type == "merge_queue" {
				observed.QueueRequired = true
			}
			if rule.Type == "required_status_checks" {
				for _, check := range rule.Parameters.Checks {
					if check.Context == "" {
						return nil
					}
					observed.RequiredChecks++
					if check.AppID != nil && *check.AppID > 0 {
						observed.AppBoundChecks++
					}
				}
			}
			// Hash provider configuration without synchronizing rule contents or check names.
			var canonical map[string]json.RawMessage
			if json.Unmarshal(raw, &canonical) != nil {
				return nil
			}
			b, _ := json.Marshal(canonical)
			definitions = append(definitions, string(b))
		}
		if len(rules) < 100 {
			sort.Strings(definitions)
			b, _ := json.Marshal(definitions)
			hash := sha256.Sum256(b)
			observed.Digest = hex.EncodeToString(hash[:])
			return &observed
		}
	}
	return nil
}
