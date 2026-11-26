// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/testserver"
)

func TestScheduledPassProcessesDurableInbox(t *testing.T) {
	token := random(32)
	s := fixtureWithIntegrations(t, testserver.Config{ScheduleToken: token})
	owner := login(t, s)
	project := owner.post(t, "projects", object{"name": "Schedule", "repository": "fixture/schedule"}, 201)["id"].(string)
	_, err := s.DB.Exec(`INSERT INTO incident_inbox(schema_version,workspace_id,project_id,source,event_key,group_key,observed,seen_at)
		VALUES(1,'personal',$1,'sentry','scheduler-fixture','scheduler-fixture','Checkout failed',now())`, project)
	if err != nil {
		t.Fatal(err)
	}
	tick := func(key string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, s.Origin+"/internal/jobs/tick", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("tick returned %d, expected %d: %s", w.Code, want, w.Body.String())
		}
	}
	processed := func() bool {
		t.Helper()
		var done bool
		if err := s.DB.QueryRow(`SELECT processed FROM incident_inbox WHERE event_key='scheduler-fixture'`).Scan(&done); err != nil {
			t.Fatal(err)
		}
		return done
	}
	tick(s.OwnerToken, 401)
	if processed() {
		t.Fatal("unauthorized request processed the inbox")
	}
	tick(token, 204)
	tick(token, 204)
	if !processed() {
		t.Fatal("scheduled request did not process inbox")
	}
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM incidents WHERE project_id=$1", project).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate or missing observation", count, err)
	}
}
