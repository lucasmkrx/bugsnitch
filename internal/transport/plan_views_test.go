// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
)

func TestPlanProgressForecastUsesOnlyCurrentAttestedIntegration(t *testing.T) {
	s := fixture(t)
	c := login(t, s)
	project := c.post(t, "projects", object{"name": "Forecast project", "repository": "test/forecast"}, 201)["id"].(string)
	p := domain.Plan{Title: "Deliver milestone", Milestone: "Milestone", Target: "2027-01-01", Forecast: "2026-12-15"}
	for _, n := range []int{1, 2, 3} {
		p.Tickets = append(p.Tickets, domain.Ticket{Number: n, Title: "Ticket", Criteria: []string{"Display the result"}, EstimateHours: 2, Verification: "Run the fixture"})
	}
	id := c.post(t, "plans", object{"projectId": project, "version": 1, "plan": p}, 201)["id"].(string)
	state := func(want string) object {
		t.Helper()
		status, res := c.req(t, "GET", "/api/v1/state", nil, "")
		if status != 200 {
			t.Fatal(status, res)
		}
		for _, row := range res["plans"].([]any) {
			v := row.(object)
			if v["id"] != id {
				continue
			}
			f := v["progressForecast"].(object)
			if f["status"] != want || v["document"].(object)["target"] != p.Target || v["document"].(object)["forecast"] != p.Forecast {
				t.Fatal(v)
			}
			return f
		}
		t.Fatal("missing plan")
		return nil
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := s.DB.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	state("unknown")
	c.post(t, "plans/"+id+"/approve", object{"version": 2}, 200)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	exec("UPDATE plan_versions SET approved_at=$2 WHERE id=$1", id, today.AddDate(0, 0, -5))
	for n := 1; n <= 2; n++ {
		c.post(t, "plans/"+id+"/evidence", object{"number": n, "revision": strings.Repeat("a", 40), "summary": "Accepted deterministic test result"}, 201)
		exec("UPDATE prerequisite_evidence SET created_at=$3 WHERE plan_id=$1 AND issue_number=$2", id, n, today.AddDate(0, 0, -4+n))
	}
	f := state("projected")
	if f["completedTickets"] != float64(2) || f["date"] != today.AddDate(0, 0, 3).Format(time.DateOnly) {
		t.Fatal(f)
	}
	// A closed or materially edited published issue must have a fresh binding.
	exec("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id)VALUES('personal',$1,22,33)", project)
	exec("INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,source_state,availability,checked_at) VALUES('personal',$1,1,11,22,33,'open','current',now())", project)
	if f = state("unknown"); f["completedTickets"] != float64(1) {
		t.Fatal("invalidated completion counted", f)
	}
	exec("INSERT INTO plan_issue_bindings(workspace_id,project_id,plan_id,number,generation) VALUES('personal',$1,$2,1,1)", project, id)
	state("projected")
}
