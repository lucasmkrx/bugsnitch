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

func TestForecastCalendarAndPausedCompletePlan(t *testing.T) {
	s := fixture(t)
	c := login(t, s)
	run := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO projects(id,workspace_id,name,repository) VALUES('p','personal','Project','test/project');
      INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order,approved_at)
      VALUES('plan','personal','p',1,'{"tickets":[{"number":3,"estimateHours":1},{"number":1,"estimateHours":5},{"number":2,"estimateHours":6}]}','[1,2,3]',now())`)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := func(offset int) time.Time { return today.AddDate(0, 0, offset) }
	run("UPDATE plan_versions SET approved_at=$1,document=jsonb_set(document,'{target}',to_jsonb($2::text))", day(-5), day(7).Format(time.DateOnly))
	run(`INSERT INTO prerequisite_evidence(workspace_id,project_id,plan_id,issue_number,revision,summary,created_at) VALUES
        ('personal','p','plan',1,'revision','verified',$1),('personal','p','plan',3,'revision','verified',$2)`, day(-3), day(-1))
	forecast := func(id string) object {
		t.Helper()
		code, data := c.req(t, "GET", "/api/v1/state", nil, "")
		if code != 200 {
			t.Fatal(code, data)
		}
		for _, row := range data["plans"].([]any) {
			plan := row.(object)
			if plan["id"] == id {
				return plan["progressForecast"].(object)
			}
		}
		t.Fatal("missing plan", id)
		return nil
	}
	check := func(state string) {
		t.Helper()
		f := forecast("plan")
		if f["status"] != state || state == "unknown" && (f["reason"] == nil || f["date"] != nil || f["targetDeltaDays"] != nil) {
			t.Fatal(f)
		}
	}
	f := forecast("plan")
	if f["date"] != day(6).Format(time.DateOnly) || f["targetDeltaDays"] != float64(-1) || f["completedEstimateHours"] != float64(6) || f["totalEstimateHours"] != float64(12) {
		t.Fatal(f)
	}
	run("UPDATE prerequisite_evidence SET created_at=$1 WHERE issue_number=3", day(-3))
	check("unknown")
	run("UPDATE prerequisite_evidence SET created_at=$1 WHERE issue_number=3", day(2))
	check("unknown")
	run("UPDATE prerequisite_evidence SET created_at=$1 WHERE issue_number=3", day(-6))
	check("unknown")
	run("UPDATE prerequisite_evidence SET created_at=$1 WHERE issue_number=3", day(-1))
	run("UPDATE projects SET paused=true")
	check("unknown")
	run(`INSERT INTO prerequisite_evidence(workspace_id,project_id,plan_id,issue_number,revision,summary,created_at)
        VALUES('personal','p','plan',2,'revision','verified',$1)`, day(0))
	f = forecast("plan")
	if f["status"] != "complete" || f["date"] != today.Format(time.DateOnly) || f["targetDeltaDays"] != float64(-7) {
		t.Fatal(f)
	}
	run("DELETE FROM prerequisite_evidence WHERE issue_number=2")
	run("UPDATE projects SET paused=false")
	run(`UPDATE plan_versions SET approved_at='1000-01-01',document=jsonb_set(document,'{tickets,2,estimateHours}','64')`)
	f = forecast("plan")
	if f["status"] != "unknown" || !strings.Contains(f["reason"].(string), "calendar range") {
		t.Fatal(f)
	}
	run(`INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order,approved_at)
       VALUES('new','personal','p',2,'{}','[]',now())`)
	f = forecast("plan")
	if !strings.Contains(f["reason"].(string), "newer") {
		t.Fatal(f)
	}
	f = forecast("new")
	if !strings.Contains(f["reason"].(string), "no estimated tickets") {
		t.Fatal(f)
	}
}
