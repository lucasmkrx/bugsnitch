// SPDX-License-Identifier: MPL-2.0
package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// RequirementsDigest binds the canonical issue title and body without syncing the body.
func (i Issue) RequirementsDigest() string {
	b, _ := json.Marshal([]string{i.Title, i.Body})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
