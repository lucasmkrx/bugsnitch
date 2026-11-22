// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/integration"
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
func (s *Server) refreshPull(call *domainCall) error {
	ws := call.Workspace
	n, e := strconv.ParseInt(call.Number, 10, 64)
	if e != nil || n < 1 {
		return bad("Invalid PR number")
	}
	var b struct{ IssueNumber, Version int64 }
	if e = call.decode(&b); e != nil {
		return e
	}
	v, e := s.pulls().Refresh(call.ctx, ws, call.Project, n, b.IssueNumber, b.Version)
	if e != nil {
		return pullError(e)
	}
	return call.reply(200, v)
}
func (s *Server) recordPull(call *domainCall) error {
	ws := call.Workspace
	n, e := strconv.ParseInt(call.Number, 10, 64)
	if e != nil || n < 1 {
		return bad("Invalid PR number")
	}
	var b integration.Evidence
	if e = call.decode(&b); e != nil {
		return e
	}
	if e = s.pulls().Record(call.ctx, ws, call.Project, n, b); e != nil {
		return pullError(e)
	}
	return call.reply(201, object{"accepted": true})
}

func (s *Server) requestDraft(call *domainCall) error {
	ws := call.Workspace
	var b integration.DraftRequest
	if err := call.decode(&b); err != nil {
		return err
	}
	d, err := s.pulls().RequestDraft(call.ctx, ws, call.Project, call.ID, b)
	if errors.Is(err, integration.ErrConflict) {
		return conflict("Draft requires a current completed attempt, validated candidate, independent review and project permission")
	}
	if errors.Is(err, integration.ErrUnavailable) {
		return problem{503, "GitHub App unavailable. Configure an installation with pull request write access."}
	}
	if err != nil {
		return pullError(err)
	}
	return call.reply(202, d)
}
