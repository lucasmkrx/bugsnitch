// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
)

func TestMonthlyUSDThresholdsBoundNewPairedTurns(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	token, device, a := runningDevice(t, owner)
	project := "projects/" + a.ProjectID + "/usage"
	read := func(path string) object { return owner.post(t, path, object{}, 200)["monthlyThreshold"].(object) }
	if r := read("usage"); r["mode"] != "off" || r["thresholdUSD"] != nil || r["reached"] != false || r["version"] != float64(0) {
		t.Fatal(r)
	}
	policy := func(path, mode string, version int, amount any, want int) object {
		return owner.post(t, path+"/threshold", object{"mode": mode, "version": version, "thresholdUSD": amount}, want)
	}
	policy("usage", "alert", 0, -1, 400)
	policy("usage", "alert", 0, 0.20, 200)
	policy("usage", "off", 0, nil, 409)
	policy(project, "stop", 0, 0.20, 200)
	// A legacy turn-limit write cannot clear the separately versioned dollar policy.
	owner.post(t, "budget", object{"monthlyTurns": nil, "version": 1}, 200)
	price := object{"model": "priced-fixture", "inputPerMillionUSD": 2, "outputPerMillionUSD": 1, "version": 0}
	owner.post(t, "usage/prices", price, 200)
	worker := func(seq int64, op string, want int) {
		t.Helper()
		status, b := owner.req(t, "POST", "/api/v1/device/admission", protocol.Admission{Protocol: 1, ProjectID: a.ProjectID, AttemptID: a.ID, IssueNumber: a.IssueNumber, BaseSHA: a.BaseSHA, Sequence: seq, Operation: op}, token)
		if status != want || status == 409 && b["error"] != "Monthly estimated model cost threshold reached" {
			t.Fatalf("worker %s/%d: %d %v", op, seq, status, b)
		}
	}
	worker(2, "turn_start", 200)
	// An in-flight turn has an unknown cost until its authenticated measurement arrives.
	if r := read(project); r["admittedTurns"] != float64(1) || r["meteredTurns"] != float64(0) || r["reached"] != false {
		t.Fatal(r)
	}
	price["inputPerMillionUSD"] = 4
	price["version"] = 1
	owner.post(t, "usage/prices", price, 200)
	meter := func(m protocol.TurnMeter) {
		t.Helper()
		status, b := owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Metering: []protocol.TurnMeter{m}}, token)
		if status != 200 {
			t.Fatal(status, b)
		}
	}
	measured := protocol.TurnMeter{Subject: a.ID, Sequence: 2, Model: "priced-fixture", Input: 100000}
	meter(measured)
	meter(measured)
	for _, path := range []string{project, "usage"} {
		if r := read(path); r["estimatedUSD"] != 0.2 || r["meteredTurns"] != float64(1) || r["pricedTurns"] != float64(1) || r["reached"] != true {
			t.Fatal(r)
		}
	}
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM events WHERE kind='budget.cost_threshold_reached'").Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	worker(2, "turn_start", 200)
	worker(3, "turn_end", 200)
	worker(4, "turn_start", 409)
	permit := func(op turns.Operation, want int) {
		t.Helper()
		status, b := owner.req(t, "POST", "/api/v1/device/model-permit", op, token)
		if status != want || status == 409 && b["error"] != "Monthly estimated model cost threshold reached" {
			t.Fatal(status, b)
		}
	}
	planner := turns.Operation{Protocol: 1, ID: random(16), Workspace: "personal", Device: device, Role: "planner", Version: 1}
	permit(planner, 200) // Workspace alert allows admissions. The project's stop covers attributed turns.
	policy("usage", "stop", 1, 0.20, 200)
	permit(planner, 200)
	manager := turns.Operation{Protocol: 1, ID: random(16), Workspace: "personal", Device: device, Role: "manager", Project: a.ProjectID, Version: 1}
	permit(manager, 409)
	planner.Version = 2
	permit(planner, 200)
	meter(protocol.TurnMeter{Subject: planner.ID, Sequence: 1, General: true, Model: "unpriced-fixture", Input: 1})
	next := planner
	next.ID = random(16)
	next.Version = 1
	permit(next, 409)
	if r := read("usage"); r["admittedTurns"] != float64(2) || r["meteredTurns"] != float64(2) || r["pricedTurns"] != float64(1) || r["estimatedUSD"] != 0.2 {
		t.Fatal(r)
	}
	policy("usage", "off", 2, nil, 200)
	permit(next, 200)
	permit(manager, 409)
	policy(project, "off", 1, nil, 200)
	permit(manager, 200)
	member := owner.post(t, "members", object{"role": "approver", "name": "release", "projectId": a.ProjectID}, 201)
	copy := *s
	copy.OwnerToken = member["credential"].(string)
	viewer := login(t, &copy)
	if _, visible := viewer.post(t, project, object{}, 200)["monthlyThreshold"]; visible {
		t.Fatal("member saw owner-only threshold")
	}
	viewer.post(t, project+"/threshold", object{"mode": "stop", "thresholdUSD": 1, "version": 2}, 403)
}

func TestMonthlyUSDConcurrentAndLateMeasurements(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	token, d1, project := runningDevice(t, owner)
	pairing := owner.post(t, "pairings", object{"name": "Second", "projectIds": []string{project.ProjectID}}, 201)["code"].(string)
	second := random(32)
	d2 := owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: pairing, Credential: second, Platform: "darwin/arm64"}, 200)["deviceId"].(string)
	owner.post(t, "usage/prices", object{"model": "measured-model", "inputPerMillionUSD": 1, "outputPerMillionUSD": 2, "version": 0}, 200)
	owner.post(t, "usage/threshold", object{"mode": "stop", "thresholdUSD": 0.2, "version": 0}, 200)
	start := func(d, token string) turns.Operation {
		t.Helper()
		o := turns.Operation{Protocol: 1, ID: random(16), Workspace: "personal", Device: d, Version: 1, Role: "planner"}
		for _, version := range []int{1, 2} {
			o.Version = version
			status, body := owner.req(t, "POST", "/api/v1/device/model-permit", o, token)
			if status != 200 {
				t.Fatal(status, body)
			}
		}
		return o
	}
	one, two := start(d1, token), start(d2, second)
	late := start(d2, second)
	// Emulate a previous month's permit whose offline reading arrived today.
	if _, err := s.DB.Exec(`UPDATE events SET created_at=date_trunc('month',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 second'
         WHERE kind='local_model.started' AND subject_id=$1`, late.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE model_price_history SET effective_at=date_trunc('month',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'-interval '1 day' WHERE model='measured-model'`); err != nil {
		t.Fatal(err)
	}
	begin := make(chan struct{})
	done := make(chan int, 4)
	for _, reading := range []struct {
		op    turns.Operation
		token string
	}{{one, token}, {one, token}, {two, second}, {late, second}} {
		go func() {
			status := 0
			defer func() { done <- status }()
			<-begin
			status, _ = owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Metering: []protocol.TurnMeter{{Subject: reading.op.ID, Sequence: 1, General: true, Model: "measured-model", Input: 100000}}}, reading.token)
		}()
	}
	close(begin)
	for range 4 {
		if code := <-done; code != 200 {
			t.Fatalf("concurrent metering request: %d", code)
		}
	}
	var past, current float64
	var notifications int
	if err := s.DB.QueryRow(`SELECT sum(estimated_usd) FILTER(WHERE month<date_trunc('month',now() AT TIME ZONE 'UTC')::date),
            sum(estimated_usd) FILTER(WHERE month=date_trunc('month',now() AT TIME ZONE 'UTC')::date),
            (SELECT count(*) FROM monthly_cost_notifications) FROM monthly_cost_usage WHERE scope=''`).Scan(&past, &current, &notifications); err != nil || past != 0.1 || current != 0.2 || notifications != 1 {
		t.Fatal(past, current, notifications, err)
	}
}
