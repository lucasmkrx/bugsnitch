// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/testserver"
)

type fixtureActivities struct {
	ids, bodies []string
	lost        bool
}

func TestLinearWebhookAuthenticatesBeforeProcessing(t *testing.T) {
	const secret = "linear-fixture-signature"
	s := fixtureWithIntegrations(t, testserver.Config{LinearSecret: secret})
	now := time.Now().UnixMilli()
	for _, tc := range []struct {
		key, event string
		when       int64
		status     int
	}{
		{"forged", "AgentSessionEvent", now, 401}, {secret, "AgentSessionEvent", now - 120000, 401},
		{secret, "Issue", now, 200}, {secret, "AgentSessionEvent", now, 503},
	} {
		body := fmt.Sprintf(`{"type":%q,"action":"created","webhookTimestamp":%d,"agentSession":{"id":"22222222-2222-4222-8222-222222222222","issue":{"id":"11111111-1111-4111-8111-111111111111"}}}`, tc.event, tc.when)
		mac := hmac.New(sha256.New, []byte(tc.key))
		mac.Write([]byte(body))
		r := httptest.NewRequest("POST", "/hooks/linear", strings.NewReader(body))
		r.Header.Set("Linear-Signature", fmt.Sprintf("%x", mac.Sum(nil)))
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s HTTP %d, expected %d: %s", tc.event, w.Code, tc.status, w.Body.String())
		}
	}
}

func (f *fixtureActivities) Activity(ctx context.Context, session, id, kind, body string) error {
	f.ids = append(f.ids, id)
	f.bodies = append(f.bodies, body)
	if f.lost {
		f.lost = false
		return errors.New("lost provider acknowledgement")
	}
	return nil
}
func TestDelegatedLinearIssueReportsHandover(t *testing.T) {
	db := fixture(t).DB
	client := &fixtureActivities{lost: true}
	s := Server{DB: db, LinearAgent: client}
	const issue = "11111111-1111-4111-8111-111111111111"
	const session = "22222222-2222-4222-8222-222222222222"
	_, e := db.Exec(`INSERT INTO projects(id,workspace_id,name,repository) VALUES('p','personal','P','org/p');
 INSERT INTO linear_issue_links(workspace_id,project_id,issue_number,linear_id,identifier,title,url,state_name,state_type,source_updated_at) VALUES('personal','p',7,'11111111-1111-4111-8111-111111111111','PR-7','show total','https://linear.app/demo/issue/PR-7','Ready','started',now())`)
	if e != nil {
		t.Fatal(e)
	}
	receive := func(prompt string) {
		b, _ := json.Marshal(object{"issue": issue, "session": session, "prompt": prompt})
		if _, err := s.receiveLinear(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
	receive("")
	receive("")
	flush := func() {
		if _, e := db.Exec("UPDATE linear_sessions SET due_at=now()"); e != nil {
			t.Fatal(e)
		}
		ok, e := s.ProcessLinearSession(t.Context())
		if !ok || e != nil {
			t.Fatal(ok, e)
		}
	}
	flush()
	flush()
	flush()
	if len(client.ids) != 2 || client.ids[0] != client.ids[1] {
		t.Fatalf("retry receipt changed: %v", client.ids)
	}
	if _, e = db.Exec(`INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version) VALUES('personal','p',10,7,1,'{"state":"MERGED","mergedSha":"sha","target":"main"}',1,0);
 INSERT INTO pull_handovers VALUES('personal','p',10,1,'sha','main','staging','build42','digest','summary','steps','owner','owner',now())`); e != nil {
		t.Fatal(e)
	}
	flush()
	if len(client.ids) != 3 || !strings.Contains(client.bodies[2], "build42") {
		t.Fatal(client.bodies)
	}
	const prompt = "33333333-3333-4333-8333-333333333333"
	receive(prompt)
	flush()
	receive(prompt)
	if ok, e := s.ProcessLinearSession(t.Context()); ok || e != nil {
		t.Fatal(ok, e)
	}
}
