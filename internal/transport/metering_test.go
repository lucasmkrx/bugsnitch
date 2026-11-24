// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestScopedModelCostsIncludeFailedAndUnmeteredTurns(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	token, _, attempt := runningDevice(t, owner)
	price := object{"model": "measured-model", "version": 0, "inputPerMillionUSD": 1, "outputPerMillionUSD": 2}
	owner.post(t, "usage/prices", price, 200)
	owner.post(t, "usage/prices", price, 409)
	claim := protocol.Admission{Protocol: 1, ProjectID: attempt.ProjectID, AttemptID: attempt.ID, IssueNumber: attempt.IssueNumber, BaseSHA: attempt.BaseSHA}
	for n, op := range []string{"turn_start", "turn_end", "turn_start", "turn_end"} {
		claim.Sequence = int64(n + 2)
		claim.Operation = op
		if status, b := owner.req(t, "POST", "/api/v1/device/admission", claim, token); status != 200 {
			t.Fatalf("admission %d %v", status, b)
		}
	}
	attempt.State = "failed"
	attempt.Version++
	reading := protocol.TurnMeter{Subject: attempt.ID, Sequence: 2, Input: 100, Output: 10, Model: "measured-model"}
	sync := func(want int) {
		t.Helper()
		status, b := owner.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Attempts: []protocol.Attempt{attempt}, Metering: []protocol.TurnMeter{reading}}, token)
		if status != want || status == 200 && b["turnMetering"] != true {
			t.Fatalf("sync %d %v", status, b)
		}
	}
	sync(200)
	sync(200)
	reading.Output++
	sync(409)
	reading.Sequence = 1
	sync(403)
	rates := object{"rates": object{"measured-model": object{"inputPerMillionUSD": 2, "outputPerMillionUSD": 5}}}
	check := func(c system, path string, want int) {
		t.Helper()
		report := c.post(t, path, rates, want)
		if want != 200 {
			return
		}
		models := report["models"].([]any)
		if report["admittedTurns"] != float64(2) || report["unmeteredTurns"] != float64(1) || report["turnsOnFailedOrCancelledAttempts"] != float64(2) ||
			len(models) != 1 || models[0].(map[string]any)["estimatedUSD"] != 0.00025 {
			t.Fatalf("usage %#v", report)
		}
	}
	check(owner, "usage", 200)
	price["version"] = 1
	price["inputPerMillionUSD"] = 100
	owner.post(t, "usage/prices", price, 200)
	actual := owner.post(t, "usage", object{"days": 30}, 200)
	metered := actual["models"].([]any)[0].(map[string]any)
	if metered["estimatedUSD"] != 0.00012 || metered["pricedTurns"] != float64(1) || actual["workspacePrices"].(map[string]any)["measured-model"].(map[string]any)["version"] != float64(2) {
		t.Fatalf("historical estimate %#v", actual)
	}
	other := owner.post(t, "projects", object{"name": "Other", "repository": "org/other"}, 201)["id"].(string)
	member := owner.post(t, "members", object{"name": "Reporter", "role": "operator", "projectId": attempt.ProjectID}, 201)
	scoped := *s
	scoped.OwnerToken = member["credential"].(string)
	viewer := login(t, &scoped)
	viewer.post(t, "usage/prices", price, 403)
	check(viewer, "usage", 403)
	check(viewer, "projects/"+other+"/usage", 403)
	check(viewer, "projects/"+attempt.ProjectID+"/usage", 200)
}
