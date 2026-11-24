// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"strings"
	"testing"
)

func TestOrderedReleaseBlocksUntilPinnedHandover(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	var stages []releaseStage
	for i, name := range []string{"backend", "frontend"} {
		p := owner.post(t, "projects", object{"name": name, "repository": "example/" + name}, 201)["id"].(string)
		head := strings.Repeat("a", 40)
		if _, e := s.DB.Exec("INSERT INTO github_mappings VALUES('personal',$1,2,$2,true)", p, i+5); e != nil {
			t.Fatal(e)
		}
		if _, e := s.DB.Exec(`INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version) VALUES('personal',$1,10,9,1,jsonb_build_object('state','OPEN','head',$2::text,'target','main'),1,0)`, p, head); e != nil {
			t.Fatal(e)
		}
		stages = append(stages, releaseStage{p, 10, head, "main"})
	}
	body := object{"name": "checkout release", "requestId": "release_fixture_id", "environment": "staging", "stages": stages}
	body["stages"] = []releaseStage{stages[0], stages[1], stages[0]}
	owner.post(t, "releases", body, 400)
	body["stages"] = stages
	owner.post(t, "releases", body, 201)
	owner.post(t, "releases", body, 201)
	hold := func(want bool) {
		var got bool
		if err := s.DB.QueryRow("SELECT EXISTS(SELECT FROM release_queue_holds WHERE project_id=$1)", stages[1].Project).Scan(&got); err != nil || got != want {
			t.Fatal(got, err)
		}
	}
	hold(true)
	merged := strings.Repeat("b", 40)
	if _, e := s.DB.Exec(`UPDATE pull_observations SET snapshot=snapshot||jsonb_build_object('state','MERGED','mergedSha',$1::text) WHERE project_id=$2`, merged, stages[0].Project); e != nil {
		t.Fatal(e)
	}
	hold(true)
	if _, e := s.DB.Exec(`INSERT INTO pull_handovers VALUES('personal',$1,10,1,$2,'main','staging','build','digest','summary','steps','owner','owner',now())`, stages[0].Project, merged); e != nil {
		t.Fatal(e)
	}
	hold(false)
	project := stages[0].Project
	for _, query := range []string{
		"INSERT INTO incidents(id,workspace_id,project_id,source,group_key,observed,expected,steps,first_seen,last_seen,occurrences) VALUES ('release-incident','personal',$1,'sentry','group','error','ok','checkout',now(),now(),1)",
		"INSERT INTO deployment_monitors(id,workspace_id,project_id,incident_id,environment,candidate,deployer_id,verifier_app_id,check_name,monitor_seconds,minimum_exposures,installation_id,repository_id,gate_hash) VALUES ('release-monitor','personal',$1,'release-incident','staging','candidate',3,4,'checkout',0,0,2,5,'gate')",
	} {
		if _, e := s.DB.Exec(query, project); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.DB.Exec("INSERT INTO deployment_holds(monitor_id,reason,evidence_key) VALUES ('release-monitor','current-build recurrence','signed-report')"); e != nil {
		t.Fatal(e)
	}
	hold(true)
	if _, e := s.DB.Exec("UPDATE deployment_holds SET cleared_at=now() WHERE monitor_id='release-monitor'"); e != nil {
		t.Fatal(e)
	}
	hold(false)
	if _, e := s.DB.Exec(`UPDATE pull_observations SET snapshot=snapshot||'{"target":"other"}'::jsonb WHERE project_id=$1`, stages[0].Project); e != nil {
		t.Fatal(e)
	}
	hold(true)
	owner.post(t, "releases/release_fixture_id/cancel", object{}, 200)
	hold(false)
}
