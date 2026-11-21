// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/planning"
	"net/http"
)

func (s *Server) publication(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		ProjectID, RequestID string
		Version              int64
		Plan                 domain.Plan
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if !keyPattern.MatchString(b.RequestID) {
		return bad("Provide a stable request ID")
	}
	if _, err := b.Plan.Order(); err != nil {
		return bad(err.Error())
	}
	id, err := (planning.Store{DB: s.DB}).Propose(r.Context(), ws, b.ProjectID, b.RequestID, b.Version, b.Plan)
	if errors.Is(err, planning.ErrConflict) {
		return conflict(err.Error())
	}
	if err != nil {
		return err
	}
	return send(w, 201, object{"id": id, "status": "proposed"})
}
func (s *Server) publish(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct{ Version int64 }
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if s.GitHubApp == nil {
		return problem{503, "Configure a GitHub App before authorizing issue publication"}
	}
	err := (planning.Store{DB: s.DB}).Approve(r.Context(), ws, r.PathValue("id"), b.Version)
	if errors.Is(err, planning.ErrConflict) || errors.Is(err, planning.ErrUnavailable) {
		return conflict(err.Error())
	}
	if err != nil {
		return err
	}
	return send(w, 202, object{"status": "accepted"})
}
