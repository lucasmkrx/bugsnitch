// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"strings"
)

// StartPass permits only a newly selected issue to advance through one ordinary
// review. Continue and restart never inherit this invocation's continuation.
func StartPass(ctx context.Context, w string, i Issue, scope protocol.RunScope, admitted func(Summary)) (Attempt, error) {
	if admitted == nil || BeforeStart == nil || CheckWorker == nil || BeforeTurn == nil || AfterTurn == nil || AfterFinish == nil || scope.Device == "" || scope.Project == "" || scope.Plan == "" || scope.Policy < 1 || i.GitHubID < 1 {
		return Attempt{}, errors.New("a local run requires an enrolled device, current approved scope and verified issue")
	}
	return create(ctx, w, i.Repository, i, "", "", &scope, admitted, nil)
}

var passPlanSchema = obj(map[string]any{"summary": str, "disposition": map[string]any{"type": "string", "enum": []string{"ready", "needs_owner"}}, "question": str})

func workerPlan(ctx context.Context, a *Attempt, files map[string]string, ordinary bool) error {
	prompt := directive + " Return a short implementation plan with a check for each criterion."
	var schema any
	if ordinary {
		prompt += " Return disposition ready with an empty question only for ordinary work within the approved issue. If scope, acceptance, architecture or a material choice needs the owner's judgment, return needs_owner with the smallest concrete question."
		schema = passPlanSchema
	}
	r, err := turn(ctx, a, prompt, map[string]any{"issue": a.Issue, "files": files, "editDirectories": a.Project.Config.EditDirectories, "pullFeedback": a.Steward}, schema)
	if err != nil {
		return err
	}
	summary := r.Text
	if ordinary {
		var plan struct{ Summary, Disposition, Question string }
		if json.Unmarshal([]byte(r.Text), &plan) != nil || strings.TrimSpace(plan.Summary) == "" || len(plan.Summary) > 8192 || len(plan.Question) > 2048 {
			return errors.New("invalid local worker plan")
		}
		question := strings.TrimSpace(plan.Question)
		if (plan.Disposition != "ready" || question != "") && (plan.Disposition != "needs_owner" || question == "") {
			return errors.New("local worker plan lacks a valid owner-decision disposition")
		}
		summary = plan.Summary
		a.OwnerQuestion = question
	}
	a.add(recordTurn(a, r, summary))
	a.Phase = "implement"
	if ordinary && !a.Project.Config.RequirePlanReview && a.OwnerQuestion == "" {
		return a.save()
	}
	a.State = "waiting"
	a.ReasonCode = protocol.PlanReviewRequired
	a.Message = "Review the plan and continue locally with any feedback."
	if a.OwnerQuestion != "" {
		a.ReasonCode = protocol.OwnerInputRequired
		a.Message = "Answer the recorded owner question with local-continue --feedback."
	}
	return nil
}

func protectedChange(path string) bool {
	lower := strings.ToLower(path)
	for _, part := range strings.Split(lower, "/") {
		stem, _, _ := strings.Cut(part, ".")
		switch stem {
		case "auth", "authorization", "authentication", "permissions", "migrations", "infrastructure", "infra", "terraform":
			return true
		}
		if part == ".github" || part == ".env" || strings.HasPrefix(part, ".env.") || strings.HasSuffix(part, ".tf") {
			return true
		}
	}
	return false
}
