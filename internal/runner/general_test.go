// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
	"strings"
	"testing"
)

func TestGeneralPermitLostEndAcknowledgment(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	oldBind, oldSend, oldModel := BindGeneral, SubmitGeneral, readOnlyModel
	defer func() { BindGeneral, SubmitGeneral, readOnlyModel = oldBind, oldSend, oldModel }()
	BindGeneral = func(ws, repo, role, id string) (turns.Operation, error) {
		return turns.Operation{Protocol: 1, ID: id, Role: role, Workspace: ws, Device: strings.Repeat("d", 32)}, nil
	}
	reads := 0
	readOnlyModel = func(context.Context, string, string, string, string, any) (agent.Result, error) {
		reads++
		return agent.Result{Text: "answer"}, nil
	}
	var seq []int
	var bound string
	lost := true
	SubmitGeneral = func(_ context.Context, op turns.Operation) error {
		if bound != "" && bound != op.ID {
			t.Fatal("reconciliation created another subject")
		}
		bound = op.ID
		seq = append(seq, op.Version)
		if op.Version == 2 && lost {
			lost = false
			return errors.New("response lost")
		}
		return nil
	}
	id, _ := newID()
	_, err := generalModel(context.Background(), "personal", "", "planner", id, "instruction", "brief", "", "", nil)
	if err == nil {
		t.Fatal("lost ack reported complete")
	}
	s, err := GeneralSessions()
	if err != nil || len(s) != 1 || s[0].Status != "ending" {
		t.Fatal(s, err)
	}
	if err = ReconcileGeneral(id, false); err != nil {
		t.Fatal(err)
	}
	if err = ReconcileGeneral(id, false); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || len(seq) != 3 || seq[0] != 1 || seq[1] != 2 || seq[2] != 2 {
		t.Fatal("unexpected replay", reads, seq)
	}
	rows, err := GeneralSessions()
	if err != nil || len(rows) != 0 {
		t.Fatal("permit retained")
	}
}
func TestManagerKeepsScopedContextAndDeduplicates(t *testing.T) {
	_, issue := fixture(t)
	oldBind := BindGeneral
	defer func() { BindGeneral = oldBind }()
	BindGeneral = func(ws, repo, role, id string) (turns.Operation, error) {
		return turns.Operation{Protocol: 1, Workspace: ws, Role: role, ID: id}, nil
	}
	old := readOnlyModel
	defer func() { readOnlyModel = old }()
	calls := 0
	readOnlyModel = func(_ context.Context, _ string, prompt string, _ string, _ string, _ any) (agent.Result, error) {
		calls++
		var v struct {
			Question           string
			RecentConversation []map[string]string
		}
		if err := json.Unmarshal([]byte(prompt), &v); err != nil {
			t.Fatal(err)
		}
		if calls == 2 && (len(v.RecentConversation) != 1 || v.RecentConversation[0]["manager"] != "First reply") {
			t.Fatal("earlier conversation missing")
		}
		text := "First reply"
		if calls > 1 {
			text = "Second reply"
		}
		return agent.Result{Text: text, Thread: "fixture", Turn: "fixture", Model: "fixture"}, nil
	}
	ctx := context.Background()
	id, _ := newID()
	first, err := Manager(ctx, "personal", issue.Repository, id, "Where is the work?", "", "")
	if err != nil || first.Status != "completed" {
		t.Fatal(first, err)
	}
	replay, err := Manager(ctx, "personal", issue.Repository, id, first.Input, "", "")
	if err != nil || calls != 1 || first.Reply != replay.Reply {
		t.Fatal("replayed input duplicated model turn")
	}
	if _, err = Manager(ctx, "personal", issue.Repository, id, "Different subject", "", ""); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	second, err := Manager(ctx, "personal", issue.Repository, "", "What is next?", "", "")
	if err != nil || second.ID == first.ID || calls != 2 {
		t.Fatal(second, err)
	}
	history, err := ManagerHistory("personal", issue.Repository)
	if err != nil || len(history) != 2 {
		t.Fatal(history, err)
	}
	if _, err = ManagerHistory("other", issue.Repository); err == nil {
		t.Fatal("wrong workspace accessed conversation")
	}
}

func TestGeneralReconciliationRequiresEvidenceOfQuiescence(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	id, _ := newID()
	op := turns.Operation{Protocol: 1, Workspace: "personal", ID: id, Role: "planner"}
	if _, err := query(generalTable + modelSlots + "INSERT INTO general_sessions VALUES(" + literal(id) + "," + packed(op) + ",'running'," + literal(now()) + "); INSERT INTO model_permits VALUES(" + literal(id) + ",'planner'," + literal(now()) + ");"); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileGeneral(id, false); err == nil {
		t.Fatal("unknown running state freed a slot")
	}
	release, err := attemptLock(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = ReconcileGeneral(id, true); err == nil {
		t.Fatal("operator confirmation overrode active CLI process")
	}
	release()
	if err = ReconcileGeneral(id, true); err != nil {
		t.Fatal(err)
	}
	b, err := query("SELECT count(*) FROM model_permits;")
	if err != nil || strings.TrimSpace(string(b)) != "0" {
		t.Fatal("ended turn retained slot")
	}
}
