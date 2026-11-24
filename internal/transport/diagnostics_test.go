// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestOwnerDiagnosticsSamplesAndReconnect(t *testing.T) {
	s := fixture(t)
	c := login(t, s)
	p := c.post(t, "projects", object{"name": "PRIVATE_PROJECT_MARKER", "repository": "local/metrics"}, 201)["id"].(string)
	code := c.post(t, "pairings", object{"name": "PRIVATE_DEVICE_MARKER", "projectIds": []string{p}}, 201)["code"].(string)
	token := random(32)
	d := c.post(t, "device/pair", protocol.Pair{Code: code, Protocol: 1, Platform: "darwin/arm64", Credential: token}, 200)["deviceId"].(string)
	if status, _ := (system{handler: c.handler}).req(t, "GET", "/api/v1/diagnostics", nil, token); status != 401 {
		t.Fatalf("device bearer read owner diagnostics: %d", status)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO workspaces(id,account_id,name) VALUES('test_other','owner','PRIVATE_FOREIGN_MARKER'); INSERT INTO projects(id,workspace_id,name,repository) VALUES('other_project','test_other','private','local/other')")
	// The live worker marks inbox rows processed in the same transaction that writes
	// incidents. Hold its write until the owner has sampled the pending rows.
	gate, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	if _, err := gate.Exec("LOCK TABLE incidents IN SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO incident_inbox(workspace_id,project_id,source,event_key,group_key,observed,seen_at,accepted_at)
	VALUES('personal',$1,'sentry','a','g','PRIVATE_INCIDENT_MARKER',now(),now()-interval '8 minutes'),
	('test_other','other_project','sentry','b','g','private',now(),now()-interval '2 hours')`, p)
	exec(`INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version,reason_code,evidence)
	VALUES('a','personal',$1,$2,7,'','','review','failed',1,'task_failed','[{"phase":"plan"},{"phase":"validate","durationMs":275},{"phase":"validate"}]')`, p, d)
	exec(`INSERT INTO general_model_turns(workspace_id,device_id,permit_id,role,version,created_at,ended_at)
	VALUES('personal',$1,'fixture','planner',2,now()-interval '5 seconds',now()-interval '2 seconds')`, d)
	check := func() (object, object) {
		t.Helper()
		status, body := c.req(t, "GET", "/api/v1/diagnostics", nil, "")
		if status != 200 {
			t.Fatal(status, body)
		}
		encoded, _ := json.Marshal(body)
		if bytes.Contains(encoded, []byte("PRIVATE_")) || bytes.Contains(encoded, []byte(token)) {
			t.Fatal("diagnostics exposed raw data")
		}
		o := body["observations"].(map[string]any)
		return body, o
	}
	body, o := check()
	if err := gate.Rollback(); err != nil {
		t.Fatal(err)
	}
	if o["contact"].(map[string]any)["observedReconnects24h"] != nil {
		t.Fatal("historical reconnect count must be unknown")
	}
	if age := body["queues"].([]any)[0].(map[string]any); age["pending"] != float64(1) || age["oldestPendingMs"].(float64) < 480000 || age["oldestPendingMs"].(float64) > 600000 {
		t.Fatal(age)
	}
	stages := o["stageActivity"].([]any)
	if len(stages) != 2 || stages[0].(map[string]any)["averageMs"] != nil || stages[1].(map[string]any)["measured"] != float64(1) || stages[1].(map[string]any)["averageMs"] != float64(275) {
		t.Fatal(stages)
	}
	if o["generalPermitHold"].([]any)[0].(map[string]any)["averageMs"] != float64(3000) || o["attemptReasons"].([]any)[0].(map[string]any)["reason"] != "task_failed" {
		t.Fatal(o)
	}
	sync := func() {
		t.Helper()
		status, response := c.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1}, token)
		if status != 200 || response["stageTimings"] != true {
			t.Fatal(status, response)
		}
	}
	sync()
	exec("UPDATE devices SET last_contact=now()-interval '3 minutes' WHERE id=$1", d)
	sync()
	sync()
	_, o = check()
	if contact := o["contact"].(map[string]any); contact["trackedDevices"] != float64(1) || contact["observedReconnects24h"] != float64(1) || contact["lastObservedReconnect24h"] == nil {
		t.Fatal(contact)
	}
}
