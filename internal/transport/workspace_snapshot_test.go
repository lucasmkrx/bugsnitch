// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func TestWorkspaceSnapshotTracksSessionAndCommittedVersion(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	_, err := s.DB.Exec(`INSERT INTO accounts(id) VALUES('other-owner');
      INSERT INTO workspaces(id,account_id,name) VALUES('other-w','other-owner','Other workspace');
      INSERT INTO projects(id,workspace_id,name,repository) VALUES('mine','personal','Mine','test/mine'),('theirs','other-w','Theirs','test/other')`)
	if err != nil {
		t.Fatal(err)
	}
	token := random(32)
	_, err = s.DB.Exec("INSERT INTO sessions(token_hash,workspace_id,expires_at) VALUES($1,'other-w',now()+interval '1 hour')", hash(token))
	if err != nil {
		t.Fatal(err)
	}
	foreignCookie := *owner.cookie
	foreignCookie.Value = token
	foreign := system{s.handler, &foreignCookie}
	status, doc := foreign.req(t, "GET", "/api/v1/state?workspace=personal", nil, "")
	if status != 200 || doc["workspace"].(object)["id"] != "other-w" || len(doc["projects"].([]any)) != 1 || doc["projects"].([]any)[0].(object)["id"] != "theirs" {
		t.Fatal(status, doc)
	}
	for _, op := range []string{"workspace.state", "workspace.diagnostics"} {
		ctx := audit.WithActor(context.Background(), audit.Plugin, "local-grant")
		err := storage.Coordinate(ctx, s.DB, op, hash(token), nil)
		var rejection *storage.CoordinationError
		if !errors.As(err, &rejection) || rejection.Status != 403 {
			t.Fatalf("%s: %v", op, err)
		}
	}
	if _, err = s.DB.Exec("UPDATE sessions SET expires_at=now()-interval '1 second' WHERE token_hash=$1", hash(token)); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"state", "diagnostics"} {
		if status, _ := foreign.req(t, "GET", "/api/v1/"+endpoint, nil, ""); status != 401 {
			t.Fatalf("%s returned %d for expired session", endpoint, status)
		}
	}

	// These two records always advance in the same committed transaction. The
	// dashboard must not combine a pre-commit workspace with a post-commit project.
	stop, done, started := make(chan struct{}), make(chan error, 1), make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			_, err := s.DB.Exec(`WITH advanced AS (UPDATE workspaces SET limits_version=limits_version+1 WHERE id='personal' RETURNING limits_version)
            UPDATE projects SET version=advanced.limits_version FROM advanced WHERE projects.id='mine'`)
			if i == 0 {
				close(started)
			}
			if err != nil {
				done <- err
				return
			}
			select {
			case <-stop:
				done <- nil
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	<-started
	defer func() {
		close(stop)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 25; i++ {
		status, doc := owner.req(t, "GET", "/api/v1/state", nil, "")
		if status != 200 {
			t.Fatal(status, doc)
		}
		projects := doc["projects"].([]any)
		if len(projects) != 1 || projects[0].(object)["id"] != "mine" || projects[0].(object)["version"] != doc["limits"].(object)["version"] {
			t.Fatal("inconsistent snapshot", doc)
		}
	}
}

func TestDashboardReadsMoreThanFourMiBOfPlans(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	_, err := s.DB.Exec(`INSERT INTO projects(id,workspace_id,name,repository) VALUES('p','personal','Project','test/project');
       WITH tickets AS (
           SELECT jsonb_agg(jsonb_build_object('number',n,'title','Ticket','verification','Check result','priority',1,'estimateHours',2,
               'resources',NULL,'dependencies',NULL,'criteria',jsonb_build_array(repeat('x',999))) ORDER BY n) AS items,
               jsonb_agg(n ORDER BY n) AS ordered FROM generate_series(1,64) n
       )
       INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order)
       SELECT 'plan-'||v,'personal','p',v,jsonb_build_object('title','Plan '||v,'assumptions','','milestone','Review','tickets',items),ordered
       FROM generate_series(1,70) v, tickets`)
	if err != nil {
		t.Fatal(err)
	}
	status, doc := owner.req(t, "GET", "/api/v1/state", nil, "")
	if status != 200 {
		t.Fatal(status, doc)
	}
	plans := doc["plans"].([]any)
	size := 0
	seen := map[string]bool{}
	for _, v := range plans {
		row := v.(object)
		seen[row["id"].(string)] = true
		for _, ticket := range row["document"].(object)["tickets"].([]any) {
			size += len(ticket.(object)["criteria"].([]any)[0].(string))
		}
	}
	if len(seen) != 70 || size != 70*64*999 || size <= 4<<20 {
		t.Fatal(len(seen), size)
	}
}

func TestDashboardFiltersUnknownFieldsInStoredDocuments(t *testing.T) {
	s := fixture(t)
	_, err := s.DB.Exec(`INSERT INTO projects(id,workspace_id,name,repository) VALUES('p','personal','Project','test/project');
    INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order)
      VALUES('plan','personal','p',1,'{"title":"Plan","assumptions":"","milestone":"M","tickets":[{"number":1,"extra":"PRIVATE_EXTRA"}]}','[1]');
    INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,policy_version,plan_version,snapshot)
      VALUES('personal','p',2,1,1,1,0,'{"extra":"PRIVATE_EXTRA","queue":{"extra":"PRIVATE_EXTRA","rules":{"extra":"PRIVATE_EXTRA"}}}');
    INSERT INTO incidents(id,workspace_id,project_id,source,group_key,observed,expected,steps,first_seen,last_seen,occurrences)
      VALUES('i','personal','p','sentry','group/path/5','Observed','Expected','Steps',now(),now(),1);
    INSERT INTO incident_inbox(workspace_id,project_id,source,event_key,group_key,observed,processed,seen_at,frames)
      VALUES('personal','p','sentry','event','group/path/5','Observed',true,now(),'[{"extra":"PRIVATE_EXTRA","file":"source.js","line":42}]')`)
	if err != nil {
		t.Fatal(err)
	}
	status, doc := login(t, s).req(t, "GET", "/api/v1/state", nil, "")
	b, err := json.Marshal(doc)
	if status != 200 || err != nil || bytes.Contains(b, []byte("PRIVATE_EXTRA")) {
		t.Fatal("dashboard included private fields or failed to load", status, err)
	}
	queue := doc["pullRequests"].([]any)[0].(object)["queue"].(object)
	if queue["ownerType"] != "" || queue["present"] != false || len(queue) != 3 || len(queue["rules"].(object)) != 4 {
		t.Fatal(queue)
	}
	ticket := doc["plans"].([]any)[0].(object)["document"].(object)["tickets"].([]any)[0].(object)
	if len(ticket) != 8 || ticket["criteria"] != nil || ticket["estimateHours"] != float64(0) {
		t.Fatal(ticket)
	}
	for _, ticket := range []string{
		`{"number":1,"title":{"private":"PRIVATE_NESTED"}}`,
		`{"number":1,"title":"t","estimateHours":1,"verification":"v","priority":1,"criteria":[{"private":"PRIVATE_NESTED"}],"resources":null,"dependencies":[]}`,
		`{"number":1,"dependencies":[{"private":"PRIVATE_NESTED"}]}`,
	} {
		_, err := s.DB.Exec("UPDATE plan_versions SET document=jsonb_set(document,'{tickets}',jsonb_build_array($1::jsonb))", ticket)
		if err != nil {
			t.Fatal(err)
		}
		status, rejected := login(t, s).req(t, "GET", "/api/v1/state", nil, "")
		raw, err := json.Marshal(rejected)
		if err != nil || status != 500 || bytes.Contains(raw, []byte("PRIVATE_NESTED")) {
			t.Fatal("malformed persisted plan was returned", status, err)
		}
	}
	// The SQL text casts would otherwise stringify an object, including private
	// keys that the typed provider contracts never accepted.
	for _, change := range []string{
		`UPDATE plan_versions SET document='{}'; UPDATE pull_observations SET snapshot='{"title":{"private":"PRIVATE_NESTED"}}'`,
		`UPDATE pull_observations SET snapshot='{"queue":{"rules":{"digest":{"private":"PRIVATE_NESTED"}}}}'`,
		`UPDATE pull_observations SET snapshot='{}'; UPDATE incident_inbox SET frames='[{"file":{"private":"PRIVATE_NESTED"}}]'`,
	} {
		if _, err := s.DB.Exec(change); err != nil {
			t.Fatal(err)
		}
		status, rejected := login(t, s).req(t, "GET", "/api/v1/state", nil, "")
		raw, err := json.Marshal(rejected)
		if err != nil || status != 500 || bytes.Contains(raw, []byte("PRIVATE_NESTED")) {
			t.Fatal("malformed stored provider field was returned", status, err)
		}
	}
}
