// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"net/http"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/turns"
)

func (s *Server) generalTurn(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var a turns.Operation
	if err := decode(w, r, &a); err != nil {
		return err
	}
	if err := compatible(a.Protocol); err != nil {
		return err
	}
	if !a.Valid() {
		return bad("Invalid local model permit")
	}
	var receipt turns.Receipt
	if err := s.coordinate(r.Context(), "claims.permit", claimInput(token, a), &receipt); err != nil {
		return err
	}
	return send(w, http.StatusOK, receipt)
}
