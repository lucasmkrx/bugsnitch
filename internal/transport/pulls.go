// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/integration"
	"net/http"
	"strconv"
)

func (s *Server) pulls() integration.Store { return integration.Store{DB: s.DB, App: s.GitHubApp} }
func pullError(e error) error {
	switch {
	case errors.Is(e, integration.ErrInvalid):
		return bad("Invalid pull request or evidence")
	case errors.Is(e, integration.ErrConflict):
		return conflict("Pull request evidence changed. Refresh the inspection.")
	case errors.Is(e, integration.ErrUnavailable):
		return problem{503, "GitHub App inspection unavailable. Check the installation mapping and read permissions."}
	case errors.Is(e, sql.ErrNoRows):
		return problem{404, "Pull request or mapping unavailable"}
	}
	return e
}
func (s *Server) refreshPull(w http.ResponseWriter, r *http.Request, ws string) error {
	n, e := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if e != nil || n < 1 {
		return bad("Invalid PR number")
	}
	var b struct{ IssueNumber, Version int64 }
	if e = decode(w, r, &b); e != nil {
		return e
	}
	v, e := s.pulls().Refresh(r.Context(), ws, r.PathValue("project"), n, b.IssueNumber, b.Version)
	if e != nil {
		return pullError(e)
	}
	return send(w, 200, v)
}
func (s *Server) recordPull(w http.ResponseWriter, r *http.Request, ws string) error {
	n, e := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if e != nil || n < 1 {
		return bad("Invalid PR number")
	}
	var b integration.Evidence
	if e = decode(w, r, &b); e != nil {
		return e
	}
	if e = s.pulls().Record(r.Context(), ws, r.PathValue("project"), n, b); e != nil {
		return pullError(e)
	}
	return send(w, 201, object{"accepted": true})
}
