// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
	"strings"
	"testing"
)

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
