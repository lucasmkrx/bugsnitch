// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"net/http"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

// Revalidation observes existing ownership and never acquires or releases
// capacity. The admission sequence fences delayed callers from prior stages.
func (s *Server) workerLease(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var a protocol.WorkerLease
	if err := decode(w, r, &a); err != nil {
		return err
	}
	if err := compatible(a.Protocol); err != nil {
		return err
	}
	if !keyPattern.MatchString(a.Attempt) || a.Issue < 1 || !shaPattern.MatchString(a.Base) || a.Sequence < 1 || a.SubjectVersion < 1 {
		return bad("Invalid worker lease subject")
	}
	var grant protocol.WorkerGrant
	if err := s.coordinate(r.Context(), "claims.lease", claimInput(token, a), &grant); err != nil {
		return err
	}
	return send(w, http.StatusOK, grant)
}
