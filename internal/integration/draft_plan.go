// SPDX-License-Identifier: MPL-2.0
package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

type DraftPlan struct {
	Version       int      `json:"version"`
	Criteria      []string `json:"criteria"`
	Verification  string   `json:"verification"`
	EstimateHours int      `json:"estimateHours"`
	Assumptions   string   `json:"assumptions,omitempty"`
	Digest        string   `json:"digest,omitempty"`
}

var ErrDraftPlanChanged = errors.New("Preview the current ticket plan before authorizing publication")

type planReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s Store) DraftPlan(ctx context.Context, workspace, project, attempt string) (*DraftPlan, error) {
	if !attemptIdentity.MatchString(attempt) {
		return nil, ErrInvalid
	}
	return readDraftPlan(ctx, s.DB, workspace, project, attempt)
}

// Scope comes from this portal start and its admission, never a newer project plan.
func readDraftPlan(ctx context.Context, db planReader, workspace, project, attempt string) (*DraftPlan, error) {
	var isPortal bool
	var planID string
	var issue, version int
	var raw []byte
	err := db.QueryRowContext(ctx, `SELECT s.id IS NOT NULL,coalesce(v.id,''),coalesce(v.version,0),v.document,a.issue_number
		FROM attempt_reports a LEFT JOIN worker_starts s ON (s.workspace_id,s.project_id,s.id)=(a.workspace_id,a.project_id,a.id)
		LEFT JOIN admission d ON (d.workspace_id,d.project_id,d.attempt_id,d.device_id,d.issue_number)=(a.workspace_id,a.project_id,a.id,a.device_id,a.issue_number)
		AND (s.device_id,s.issue_number,s.plan_id)=(d.device_id,d.issue_number,d.plan_id)
		LEFT JOIN plan_versions v ON (v.workspace_id,v.project_id,v.id)=(a.workspace_id,a.project_id,d.plan_id) AND v.approved_at IS NOT NULL
		WHERE a.workspace_id=$1 AND a.project_id=$2 AND a.id=$3`, workspace, project, attempt).Scan(&isPortal, &planID, &version, &raw, &issue)
	if err != nil || !isPortal {
		return nil, err
	}
	var doc domain.Plan
	if planID == "" || json.Unmarshal(raw, &doc) != nil {
		return nil, ErrDraftPlanChanged
	}
	if _, err = doc.Order(); err != nil {
		return nil, ErrDraftPlanChanged
	}
	for _, ticket := range doc.Tickets {
		if ticket.Number != issue {
			continue
		}
		out := DraftPlan{Version: version, EstimateHours: ticket.EstimateHours, Verification: incidents.Redact(ticket.Verification)}
		for _, criterion := range ticket.Criteria {
			out.Criteria = append(out.Criteria, incidents.Redact(criterion))
		}
		if len(doc.Tickets) == 1 && doc.Assumptions == incidents.Redact(doc.Assumptions) {
			out.Assumptions = doc.Assumptions
		}
		// Bind the rendered contents and the originating immutable plan and attempt.
		identity, err := json.Marshal([]any{planID, attempt, issue, out})
		if err != nil {
			return nil, err
		}
		out.Digest = fmt.Sprintf("%x", sha256.Sum256(identity))
		return &out, nil
	}
	return nil, ErrDraftPlanChanged
}

// Put ticket prose inside a literal Markdown fence. Pick the shorter safe fence
// to bound expansion even when user text consists entirely of markup.
func literalPlanText(text string) string {
	fence := ""
	for _, mark := range []byte{'`', '~'} {
		run, longest := 0, 2
		for i := 0; i < len(text); i++ {
			if text[i] == mark {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		candidate := strings.Repeat(string(mark), longest+1)
		if fence == "" || len(candidate) < len(fence) {
			fence = candidate
		}
	}
	return fence + "\n" + text + "\n" + fence
}

func (p *DraftPlan) approvedExcerpt(request DraftRequest) (string, error) {
	if p == nil {
		if request.PlanDigest != "" || request.SharePlanAssumptions {
			return "", ErrDraftPlanChanged
		}
		return "", nil
	}
	if request.PlanDigest != p.Digest || request.SharePlanAssumptions && p.Assumptions == "" {
		return "", ErrDraftPlanChanged
	}
	var criteria []string
	for i, text := range p.Criteria {
		criteria = append(criteria, fmt.Sprintf("%d. %s", i+1, text))
	}
	unit := "hours"
	if p.EstimateHours == 1 {
		unit = "hour"
	}
	body := fmt.Sprintf("## Approved ticket plan (v%d)\n\nEstimated effort is %d %s.\n\n### Acceptance criteria\n\n%s\n\n### Planned verification\n\n%s\n\n", p.Version, p.EstimateHours, unit, literalPlanText(strings.Join(criteria, "\n")), literalPlanText(p.Verification))
	if request.SharePlanAssumptions {
		body += "### Ticket assumptions\n\n" + literalPlanText(p.Assumptions) + "\n\n"
	}
	return body, nil
}
