// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
	"strings"
	"testing"
)

func TestOwnerSetsDeviceTurnCapacity(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	token, device, report := runningDevice(t, owner)
	deviceLimits := "devices/" + device + "/limits"
	workspace := owner.post(t, "limits", object{"tickets": 4, "turns": 4, "version": 1}, 200)
	turn := func(op turns.Operation, want int) {
		t.Helper()
		if status, result := owner.req(t, "POST", "/api/v1/device/model-permit", op, token); status != want {
			t.Fatalf("permit returned %d, want %d: %v", status, want, result)
		}
	}
	permit := func() turns.Operation {
		return turns.Operation{Protocol: 1, ID: random(16), Workspace: "personal", Device: device, Role: "planner", Version: 1}
	}
	first, second, third, fourth := permit(), permit(), permit(), permit()
	turn(first, 200)
	turn(second, 200)
	turn(third, 409) // The device still defaults to two when its workspace allows four.
	start := protocol.Admission{Protocol: 1, ProjectID: report.ProjectID, AttemptID: report.ID, IssueNumber: report.IssueNumber, BaseSHA: report.BaseSHA, Operation: "turn_start", Sequence: 2}
	if status, body := owner.req(t, "POST", "/api/v1/device/admission", start, token); status != 409 || body["code"] != "model_turn_capacity" || body["error"] != "Model-turn capacity is reserved" {
		t.Fatalf("unexpected worker capacity response: %d %v", status, body)
	}
	owner.post(t, deviceLimits, object{"turns": 33, "version": workspace["version"]}, 400)
	raised := owner.post(t, deviceLimits, object{"turns": 4, "version": workspace["version"]}, 200)
	owner.post(t, deviceLimits, object{"turns": 1, "version": workspace["version"]}, 409)
	turn(third, 200)
	turn(fourth, 200)
	turn(permit(), 409)
	status, snapshot := owner.req(t, "GET", "/api/v1/state", nil, "")
	if status != 200 || snapshot["devices"].([]any)[0].(object)["turnLimit"] != float64(4) || snapshot["limits"].(object)["version"] != raised["version"] {
		t.Fatal("expected the device capacity and its matching version in the workspace snapshot")
	}

	limited := owner.post(t, "limits", object{"tickets": 4, "turns": 3, "version": raised["version"]}, 200)
	fourth.Version = 2
	turn(fourth, 200)
	turn(permit(), 409) // Workspace limit is now lower than the device's four turns.
	lowered := owner.post(t, deviceLimits, object{"turns": 1, "version": limited["version"]}, 200)
	second.Version, third.Version = 2, 2
	turn(second, 200)
	turn(third, 200)
	turn(first, 200) // Existing turns can replay and finish after a reduction.
	turn(permit(), 409)
	first.Version = 2
	turn(first, 200)
	turn(permit(), 200)

	if _, err := s.DB.Exec(`INSERT INTO workspaces(id,account_id,name) VALUES('foreign','owner','Other');
		INSERT INTO devices(id,workspace_id,name,platform) VALUES('other-device','foreign','Other','darwin/arm64')`); err != nil {
		t.Fatal(err)
	}
	owner.post(t, "devices/other-device/limits", object{"turns": 5, "version": lowered["version"]}, 404)
	owner.post(t, "devices/"+device+"/revoke", object{}, 200)
	owner.post(t, deviceLimits, object{"turns": 5, "version": lowered["version"]}, 404)
}

func TestGeneralAndWorkerTurnsShareOneBudget(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	project := owner.post(t, "projects", object{"name": "Selected", "repository": "fixture/turns"}, 201)["id"].(string)
	if _, err := s.DB.Exec("UPDATE workspaces SET turn_limit=1 WHERE id='personal'"); err != nil {
		t.Fatal(err)
	}
	pair := func() (string, string) {
		code := owner.post(t, "pairings", object{"name": "Computer", "projectIds": []string{project}}, 201)["code"].(string)
		tk := random(32)
		d := owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: tk, Platform: "darwin/arm64"}, 200)["deviceId"].(string)
		return d, tk
	}
	d1, tk1 := pair()
	d2, tk2 := pair()
	op := turns.Operation{Protocol: 1, ID: random(16), Workspace: "personal", Device: d1, Role: "planner", Version: 1}
	permit := func(tk string, a turns.Operation) int {
		code, _ := owner.req(t, "POST", "/api/v1/device/model-permit", a, tk)
		return code
	}
	worker := func(kind string, seq int64) int {
		code, _ := owner.req(t, "POST", "/api/v1/device/admission", protocol.Admission{Protocol: 1, ProjectID: project, AttemptID: "worker_ticket", IssueNumber: 8, BaseSHA: strings.Repeat("d", 40), Operation: kind, Sequence: seq}, tk2)
		return code
	}
	if worker("admit", 1) != 200 {
		t.Fatal("worker not admitted")
	}
	general, other := make(chan int, 1), make(chan int, 1)
	go func() { general <- permit(tk1, op) }()
	go func() { other <- worker("turn_start", 2) }()
	a, b := <-general, <-other
	if a == 200 && b == 409 {
		if permit(tk1, op) != 200 {
			t.Fatal("lost start ack not reconciled")
		}
		if _, err := s.DB.Exec("UPDATE devices SET last_contact=now()-interval '2 hours'"); err != nil {
			t.Fatal(err)
		}
		if worker("turn_start", 2) != 409 {
			t.Fatal("disconnect freed a general slot")
		}
		op.Version = 2
		if permit(tk1, op) != 200 || permit(tk1, op) != 200 || worker("turn_start", 2) != 200 {
			t.Fatal("lost end ack or worker admission")
		}
		op.Version = 1
		if permit(tk1, op) != 409 {
			t.Fatal("stale start resurrected original turn")
		}
		op.ID = random(16)
	} else if a != 409 || b != 200 {
		t.Fatalf("race admitted %d and %d", a, b)
	}
	if permit(tk1, op) != 409 || worker("turn_end", 3) != 200 || permit(tk1, op) != 200 {
		t.Fatal("general turn bypassed worker allocation or did not resume")
	}
	wrong := op
	wrong.Version = 2
	wrong.Device = d2
	if permit(tk2, wrong) != 403 {
		t.Fatal("another device released permit")
	}
	wrong.Device = d1
	if permit(tk2, wrong) != 403 {
		t.Fatal("device claim spoofed")
	}
	wrong.Workspace = "foreign"
	if permit(tk1, wrong) != 403 {
		t.Fatal("workspace mismatch accepted")
	}
	op.Version = 2
	if permit(tk1, op) != 200 {
		t.Fatal("release")
	}
	never := op
	never.ID = random(16)
	if permit(tk1, never) != 200 {
		t.Fatal("out-of-order release")
	}
	never.Version = 1
	if permit(tk1, never) != 409 {
		t.Fatal("late acquire bypassed tombstone")
	}
	manager := turns.Operation{Protocol: 1, ID: random(16), Workspace: "personal", Device: d1, Role: "manager", Version: 1, Project: random(16)}
	if permit(tk1, manager) != 403 {
		t.Fatal("unassigned project manager admitted")
	}
	manager.Project = project
	if permit(tk1, manager) != 200 {
		t.Fatal("assigned manager rejected")
	}
	if _, err := s.DB.Exec("UPDATE workspaces SET stopped=true WHERE id='personal'"); err != nil {
		t.Fatal(err)
	}
	if permit(tk1, manager) != 409 {
		t.Fatal("replaying an active grant bypassed workspace emergency stop")
	}
	manager.Version = 2
	if permit(tk1, manager) != 200 {
		t.Fatal("emergency stop prevented demonstrated release")
	}
	manager.Version = 1
	owner.post(t, "devices/"+d1+"/revoke", object{}, 200)
	manager.ID = random(16)
	if permit(tk1, manager) != 403 {
		t.Fatal("revoked computer admitted")
	}
}

func TestWorkspaceMonthlyTurnBudget(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	token, device, _ := runningDevice(t, owner)
	owner.post(t, "budget", object{"monthlyTurns": 1, "version": 1}, 200)
	send := func(o turns.Operation, want int) {
		t.Helper()
		code, r := owner.req(t, "POST", "/api/v1/device/model-permit", o, token)
		if code != want {
			t.Fatal(code, r)
		}
	}
	turn := turns.Operation{Protocol: 1, ID: random(16), Workspace: "personal", Device: device, Role: "planner", Version: 1}
	send(turn, 200)
	send(turn, 200)
	turn.Version = 2
	send(turn, 200)
	turn.ID = random(16)
	turn.Version = 1
	send(turn, 409)
	_, state := owner.req(t, "GET", "/api/v1/state", nil, "")
	if state["budget"].(object)["usedTurns"] != float64(1) {
		t.Fatal(state["budget"])
	}
	owner.post(t, "budget", object{"monthlyTurns": nil, "version": 2}, 200)
	send(turn, 200)
}
