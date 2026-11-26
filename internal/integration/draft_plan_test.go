// SPDX-License-Identifier: MPL-2.0
package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestDraftUsesApprovedPortalTicketExcerpt(t *testing.T) {
	db, exec := scratchDB(t)
	ctx := t.Context()
	s := Store{DB: db, App: &github.App{}}
	id, head, base := strings.Repeat("1", 32), strings.Repeat("a", 40), strings.Repeat("b", 40)
	const updated = "2026-11-26T12:00:00Z"
	const criterion = "Keeps the receipt\n```\n## Untrusted heading"
	ticket := domain.Ticket{Number: 17, Title: "Keep receipt", Criteria: []string{criterion, "api_key=privatecriterion"}, Verification: "Run receipt tests", EstimateHours: 3}
	single := domain.Plan{Title: "Receipt fix", Milestone: "Next release", Assumptions: "Receipt IDs remain stable", Tickets: []domain.Ticket{ticket}}
	second := ticket
	second.Number, second.Title, second.Verification = 18, "Unrelated issue", "Other private test plan"
	multiple := single
	multiple.Tickets = []domain.Ticket{ticket, second}
	multiple.Assumptions = "Project-wide private assumptions"
	for index, doc := range []domain.Plan{single, multiple} {
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		exec("INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order,approved_at) VALUES ($1,'personal','p',$2,$3,'[]',now())", []string{"first", "second"}[index], index+1, string(raw))
	}
	exec(`INSERT INTO devices(id,workspace_id,name,platform) VALUES('d','personal','fixture','test');
		INSERT INTO device_projects(workspace_id,device_id,project_id) VALUES('personal','d','p');
		INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,availability,source_state,checked_at) VALUES('personal','p',17,77,10,11,'current','open',now());
		INSERT INTO plan_issue_bindings(workspace_id,project_id,plan_id,number,generation) VALUES('personal','p','first',17,1),('personal','p','second',17,1)`)
	exit := 0
	evidence, _ := json.Marshal([]protocol.StageEvidence{{Phase: "validate", CandidateSHA: head, ExitCode: &exit}, {Phase: "review", CandidateSHA: head}})
	exec(`INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version,evidence,issue_updated_at)
		VALUES($1,'personal','p','d',17,$2,$3,'wrap_up','completed',5,$4,$5)`, id, base, head, string(evidence), updated)
	preview := func() *DraftPlan {
		t.Helper()
		p, err := s.DraftPlan(ctx, "personal", "p", id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if preview() != nil {
		t.Fatal("nonportal attempt gained a disclosure requirement")
	}
	exec(`INSERT INTO worker_starts(id,workspace_id,project_id,device_id,issue_number,request_id,requested_plan,policy_version,github_id,digest,revision,title,plan_id,status)
		VALUES($1,'personal','p','d',17,'request','first',1,77,'digest',$2,'Receipt','first','started')`, id, updated)
	exec(`INSERT INTO admission(workspace_id,project_id,device_id,attempt_id,issue_number,base_sha,sequence,operation,active,in_turn,plan_id)
		VALUES('personal','p','d',$1,17,$2,1,'release',false,false,'first')`, id, base)
	initial := preview()
	if initial == nil || initial.Version != 1 || initial.EstimateHours != 3 || initial.Assumptions != single.Assumptions || initial.Criteria[1] != "[redacted]" {
		t.Fatal(initial)
	}
	request := DraftRequest{Target: "main", Version: 5, PolicyVersion: 1, Candidate: head, Delivery: "local_ship"}
	approve := func(want error) {
		t.Helper()
		_, err := s.RequestDraft(ctx, "personal", "p", id, request)
		if !errors.Is(err, want) {
			t.Fatal(err, want)
		}
	}
	approve(ErrDraftPlanChanged)
	request.PlanDigest = initial.Digest
	without, err := initial.approvedExcerpt(request)
	if err != nil || strings.Contains(without, single.Assumptions) {
		t.Fatal("assumptions were not opt-in")
	}
	request.SharePlanAssumptions = true
	approve(nil)
	approve(nil)
	var excerpt string
	var count int
	if err = db.QueryRow("SELECT plan_excerpt,(SELECT count(*) FROM events WHERE kind='draft.requested') FROM draft_pr_intents WHERE attempt_id=$1", id).Scan(&excerpt, &count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	body := (job{c: candidate{issue: 17, head: head, base: base}, planExcerpt: excerpt}).body()
	for _, want := range []string{"Estimated effort is 3 hours.", "~~~\n1. " + criterion, "2. [redacted]\n~~~", "Run receipt tests", single.Assumptions} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing approved PR content %q", want)
		}
	}
	if strings.Count(body, "## Approved ticket plan") != 1 || strings.Contains(body, "privatecriterion") || strings.Contains(body, second.Verification) {
		t.Fatal("unapproved plan content in draft")
	}
	request.SharePlanAssumptions = false
	approve(ErrConflict)
	exec("UPDATE admission SET plan_id='second' WHERE attempt_id=$1", id)
	if _, err = s.DraftPlan(ctx, "personal", "p", id); !errors.Is(err, ErrDraftPlanChanged) {
		t.Fatal("inconsistent admission accepted", err)
	}
	exec("UPDATE worker_starts SET plan_id='second' WHERE id=$1", id)
	newer := preview()
	if newer.Version != 2 || newer.Assumptions != "" || newer.Digest == initial.Digest {
		t.Fatal("new plan or unrelated assumptions", newer)
	}
	approve(ErrDraftPlanChanged)
	request.PlanDigest, request.SharePlanAssumptions = newer.Digest, true
	approve(ErrDraftPlanChanged)
}
