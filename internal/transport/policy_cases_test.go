// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"strings"
	"testing"
)

func TestHistoricalPolicyExportPinsPremergeRevisions(t *testing.T) {
	s := fixture(t)
	o := login(t, s)
	p := o.post(t, "projects", object{"name": "Web", "repository": "test/web"}, 201)["id"].(string)
	head, base, merged := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	if _, e := s.DB.Exec(`INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version)
 VALUES('personal',$1,7,6,1,jsonb_build_object('state','OPEN','head',$2::text,'base',$3::text,'target','main'),1,0)`, p, head, base); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB.Exec(`UPDATE pull_observations SET snapshot=snapshot||jsonb_build_object('state','MERGED','base',$1::text,'mergedSha',$1::text) WHERE project_id=$2`, merged, p); e != nil {
		t.Fatal(e)
	}
	label := object{"label": "clean", "cohort": "holdout", "reference": strings.Repeat("1", 64), "head": head, "base": merged, "target": "main"}
	path := "projects/" + p + "/pulls/7/policy-case"
	o.post(t, path, label, 409)
	label["base"] = base
	o.post(t, path, label, 200)
	o.post(t, path, label, 200)
	label["cohort"] = "calibration"
	o.post(t, path, label, 409)
	status, out := o.req(t, "GET", "/api/v1/projects/"+p+"/policy-cases", nil, "")
	if status != 200 {
		t.Fatal(status, out)
	}
	rows := out["cases"].([]any)
	if len(rows) != 1 || rows[0].(object)["base"] != base || rows[0].(object)["head"] != head || rows[0].(object)["evidence"] != nil {
		t.Fatal(rows)
	}
	for _, q := range []string{
		"INSERT INTO github_mappings VALUES('personal',$1,21,22,true)",
		"INSERT INTO incidents(id,workspace_id,project_id,source,group_key,observed,expected,steps,first_seen,last_seen,occurrences) VALUES ('policy-incident','personal',$1,'sentry','threshold','error','ok','checkout',now(),now(),1)",
		"INSERT INTO incident_issues(workspace_id,project_id,incident_id,installation_id,repository_id,marker,approved_version,status,number) VALUES('personal',$1,'policy-incident',21,22,'test-marker',1,'linked',6)",
		"INSERT INTO deployment_monitors(id,workspace_id,project_id,incident_id,environment,candidate,deployer_id,verifier_app_id,check_name,monitor_seconds,minimum_exposures,installation_id,repository_id,gate_hash) VALUES ('policy-monitor','personal',$1,'policy-incident','staging','unrelated',3,4,'threshold',0,0,21,22,'gate')",
	} {
		if _, err := s.DB.Exec(q, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec("INSERT INTO deployment_holds(monitor_id,reason,evidence_key) VALUES ('policy-monitor','current-build recurrence','signed-observation')"); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		status, report := o.req(t, "GET", "/api/v1/projects/"+p+"/policy-cases", nil, "")
		if status != 200 {
			t.Fatal(status, report)
		}
		row := report["cases"].([]any)[0].(object)
		if row["label"] != want || row["base"] != base || row["cohort"] != "holdout" || want == "regression" && !strings.HasPrefix(row["labelReference"].(string), "deployment-hold/") {
			t.Fatal(row)
		}
	}
	check("clean")
	if _, err := s.DB.Exec("UPDATE deployment_monitors SET candidate=$1 WHERE id='policy-monitor'", merged); err != nil {
		t.Fatal(err)
	}
	check("regression")
}
