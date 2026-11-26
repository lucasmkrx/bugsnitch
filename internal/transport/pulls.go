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
	case errors.Is(e, integration.ErrDraftPlanChanged):
		return conflict(integration.ErrDraftPlanChanged.Error())
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

func (s *Server) previewDraftPlan(call *domainCall) error {
	plan, err := s.pulls().DraftPlan(call.ctx, call.Workspace, call.Project, call.ID)
	if err != nil {
		return pullError(err)
	}
	return call.reply(200, object{"plan": plan})
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

func (s *Server) recordHandover(call *domainCall) error {
	n, err := strconv.ParseInt(call.Number, 10, 64)
	if err != nil {
		return bad("Invalid PR number")
	}
	var b integration.Handover
	if err = call.decode(&b); err != nil {
		return err
	}
	if err = s.pulls().Handover(call.ctx, call.Workspace, call.Project, n, b); err != nil {
		if errors.Is(err, integration.ErrUnverifiedDeployment) {
			return conflict(err.Error())
		}
		return pullError(err)
	}
	return call.reply(201, object{"accepted": true})
}

func (s *Server) watchPull(call *domainCall) error {
	n, err := strconv.ParseInt(call.Number, 10, 64)
	if err != nil {
		return bad("Invalid PR number")
	}
	var b struct {
		Version  int64
		Watching *bool
	}
	if err := call.decode(&b); err != nil {
		return err
	}
	if b.Watching == nil {
		return bad("Choose whether to watch this pull request")
	}
	if err := s.pulls().Watch(call.ctx, call.Workspace, call.Project, n, b.Version, *b.Watching); err != nil {
		return pullError(err)
	}
	return call.reply(200, object{"watching": *b.Watching})
}

func (s *Server) approvePull(call *domainCall) error {
	n, err := strconv.ParseInt(call.Number, 10, 64)
	if err != nil {
		return bad("Invalid PR number")
	}
	var b integration.SignOff
	if err = call.decode(&b); err != nil {
		return err
	}
	err = s.pulls().Approve(call.ctx, call.Workspace, call.Project, n, b)
	if errors.Is(err, integration.ErrConflict) {
		return conflict("Sign-off requires the current head and target, passing checks, GitHub approval and recorded review and validation evidence.")
	}
	if err != nil {
		return pullError(err)
	}
	return call.reply(201, object{"approved": true})
}

func (s *Server) queuePull(call *domainCall) error {
	n, err := strconv.ParseInt(call.Number, 10, 64)
	if err != nil {
		return bad("Invalid PR number")
	}
	var b integration.QueueRequest
	if err = call.decode(&b); err != nil {
		return err
	}
	decision, err := s.pulls().Queue(call.ctx, call.Workspace, call.Project, n, b)
	if err != nil {
		return pullError(err)
	}
	return call.reply(200, decision)
}
