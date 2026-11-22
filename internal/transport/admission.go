// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"net/http"
	"slices"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

// Admission is requested by a human-initiated local execution. No command starts a model remotely.
func (s *Server) admission(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var a protocol.Admission
	if err := decode(w, r, &a); err != nil {
		return err
	}
	if err := compatible(a.Protocol); err != nil {
		return err
	}
	if a.Sequence < 1 || !keyPattern.MatchString(a.AttemptID) || !shaPattern.MatchString(a.BaseSHA) || a.IssueNumber < 1 || !slices.Contains([]string{"admit", "finish", "turn_start", "turn_end"}, a.Operation) {
		return bad("Invalid admission request")
	}
	if a.Operation == "finish" && !slices.Contains([]string{"waiting", "completed", "failed", "cancelled"}, a.State) {
		return bad("Provide the demonstrated local outcome")
	}
	var receipt struct {
		Sequence int64 `json:"sequence"`
	}
	if err := s.coordinate(r.Context(), "claims.admission", claimInput(token, a), &receipt); err != nil {
		return err
	}
	return send(w, http.StatusOK, receipt)
}

// Rust receives proof of device identity as a digest. The bearer credential
// remains in the authenticated HTTP request.
func claimInput(token string, request any) object {
	return object{"tokenHash": hash(token), "request": request}
}
