// SPDX-License-Identifier: MPL-2.0
package device

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func TestUnattendedTurnBudgetIsAtomicAndDurable(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	grant := unattendedGrant{
		unattendedScope: unattendedScope{Scope: protocol.RunScope{Project: "project"}, TurnBudget: 2},
		ID:              "example",
		Expires:         time.Now().Add(time.Hour),
	}
	data, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal(unattendedSchema + "INSERT INTO unattended(project,id,data) VALUES('project','example'," + sqlValue(string(data)) + ");"); err != nil {
		t.Fatal(err)
	}
	const attempts = 8
	results := make(chan error, attempts)
	for range attempts {
		go func() { results <- grant.takeTurn(t.Context()) }()
	}
	var started int
	for range attempts {
		err := <-results
		if err == nil {
			started++
		} else if !errors.Is(err, runner.ErrUnattendedBudget) {
			t.Error(err)
		}
	}
	status, err := readUnattended(t.Context(), grant.Scope.Project)
	if err != nil || status.TurnsUsed != 2 || started != 2 {
		t.Fatal(started, status, err)
	}
	if err := status.Grant.takeTurn(t.Context()); !errors.Is(err, runner.ErrUnattendedBudget) {
		t.Fatal("reloaded grant exceeded its durable budget", err)
	}
	if err := updateUnattended(t.Context(), status.Grant, "operator stopped", true); err != nil {
		t.Fatal(err)
	}
	if err := status.Grant.takeTurn(t.Context()); !errors.Is(err, runner.ErrAuthorityStale) {
		t.Fatal("stopped grant started a model turn", err)
	}
}

func TestUnattendedRetriesOnlyReconcileTheSameDurableAction(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	grant := unattendedGrant{
		unattendedScope: unattendedScope{Scope: protocol.RunScope{Project: "project"}},
		ID:              "test-grant", Expires: time.Now().Add(time.Hour),
	}
	data, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal(unattendedSchema + "INSERT INTO unattended(project,id,data) VALUES('project','test-grant'," + sqlValue(string(data)) + ");"); err != nil {
		t.Fatal(err)
	}
	a := runner.Attempt{Summary: runner.Summary{ID: "attempt", Version: 3}}
	var stopped, reconciled int
	for range 6 {
		_ = grant.effect(t.Context(), a, "resume_plan", false, func() error { stopped++; return errors.New("unknown model outcome") })
		_ = grant.effect(t.Context(), a, "publish:2", true, func() error { reconciled++; return errors.New("unknown push outcome") })
	}
	if stopped != 1 || reconciled != 3 {
		t.Fatal(stopped, reconciled)
	}
	state, err := journal("SELECT state FROM unattended_actions WHERE action='publish:2';")
	if state != "held" || err != nil {
		t.Fatal(state, err)
	}
	if err := updateUnattended(t.Context(), grant, "operator stopped", true); err != nil {
		t.Fatal(err)
	}
	if err := grant.effect(t.Context(), a, "publish:3", true, func() error { t.Error("stopped grant acted"); return nil }); err != nil {
		t.Fatal(err)
	}
}
