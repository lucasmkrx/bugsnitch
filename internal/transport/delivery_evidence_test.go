// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func TestIncidentRelatedWorkAndExistingIssueLink(t *testing.T) {
	s := fixture(t)
	o := login(t, s)
	p := o.post(t, "projects", object{"name": "Web", "repository": "example/web"}, 201)["id"].(string)
	id := o.post(t, "incidents", object{"projectId": p, "requestId": "checkout_report", "observed": "Checkout shipping threshold charges a fee", "expected": "Free shipping at threshold"}, 201)["id"].(string)
	path := "projects/" + p + "/incidents/" + id
	if code, _ := o.req(t, "GET", "/api/v1/"+path+"/related-work", nil, ""); code != 503 {
		t.Fatal(code)
	}
	if _, err := s.DB.Exec(`INSERT INTO github_mappings VALUES ('personal',$1,22,31,true)`, p); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "fixture.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /app":
			fmt.Fprint(w, `{"id":1,"slug":"fixture"}`)
		case "POST /app/installations/22/access_tokens":
			var input struct{ Permissions map[string]string }
			if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Permissions) != 1 || input.Permissions["issues"] != "read" {
				t.Error("triage did not request the repository's read-only issue token")
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{"token":"fixture"}`)
		case "GET /repositories/31":
			fmt.Fprint(w, `{"id":31,"full_name":"example/web"}`)
		case "GET /repos/example/web/issues":
			if r.URL.Query().Get("state") != "open" || r.URL.Query().Get("sort") != "updated" {
				t.Error("unexpected issue list filters")
			}
			fmt.Fprint(w, `[{"id":54,"number":4,"title":"Document profile settings","state":"open"},
				{"id":57,"number":7,"title":"Checkout shipping threshold","body":"The fee changes at $50.","state":"open"},
				{"id":58,"number":8,"title":"Repair shipping threshold","state":"open","pull_request":{}}]`)
		case "GET /repos/example/web/issues/7":
			fmt.Fprint(w, `{"id":57,"number":7,"title":"Checkout shipping threshold","state":"open"}`)
		case "GET /repos/example/web/issues/8":
			fmt.Fprint(w, `{"id":58,"number":8,"title":"Repair shipping threshold","state":"open","pull_request":{}}`)
		default:
			t.Errorf("unexpected GitHub call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	app, err := github.NewLoopbackFixture("fixture", file, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	service := Server{DB: s.DB, GitHubApp: app, Incidents: incidents.Store{DB: s.DB}}
	call := func(op string, body object, status int) domainValue {
		t.Helper()
		input, err := json.Marshal(object{"workspace": "personal", "project": p, "id": id, "body": body})
		if err != nil {
			t.Fatal(err)
		}
		response := service.Domain(t.Context(), DomainRequest{Version: 1, Operation: op, Actor: [2]string{"owner", "owner"}, Input: input})
		if response.Status != status {
			t.Fatalf("%s: %d %s", op, response.Status, response.Error)
		}
		result, _ := response.Value.(domainValue)
		return result
	}
	items := call("incident.related_work", nil, 200).Body.(object)["items"].([]workSuggestion)
	if len(items) != 2 || items[0].Number != 7 || items[0].Pull || items[1].Number != 8 || !items[1].Pull {
		t.Fatal(items)
	}
	call("incident.link_issue", object{"number": 8, "version": 1}, 409)
	call("incident.link_issue", object{"number": 7, "version": 2}, 409)
	call("incident.link_issue", object{"number": 7, "version": 1}, 200)
	call("incident.link_issue", object{"number": 7, "version": 1}, 200)
	var matched int
	if err := s.DB.QueryRow(`SELECT count(*) FROM incident_issues WHERE incident_id=$1 AND number=7 AND issue_id=57 AND status='linked'`, id).Scan(&matched); err != nil || matched != 1 {
		t.Fatal(matched, err)
	}
}

func TestBrowserQAUploadIsScopedToIncidentAndExactPullHead(t *testing.T) {
	s := fixture(t)
	o := login(t, s)
	p := o.post(t, "projects", object{"name": "Web", "repository": "example/web"}, 201)["id"].(string)
	id := o.post(t, "incidents", object{"projectId": p, "requestId": "checkout_report", "observed": "Unexpected shipping fee", "expected": "Free shipping"}, 201)["id"].(string)
	base, head, attempt := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 32)
	code := o.post(t, "pairings", object{"name": "QA runner", "projectIds": []string{p}}, 201)["code"].(string)
	token := random(32)
	dev := o.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)["deviceId"].(string)
	for _, q := range []string{
		"INSERT INTO github_mappings VALUES('personal',$1,21,22,true)",
		"INSERT INTO incident_issues(workspace_id,project_id,incident_id,installation_id,repository_id,marker,approved_version,status,number) VALUES('personal',$1,$2,21,22,'qa-marker',1,'linked',6)",
		"INSERT INTO deployment_monitors(id,workspace_id,project_id,incident_id,environment,candidate,deployer_id,verifier_app_id,check_name,monitor_seconds,minimum_exposures,installation_id,repository_id,gate_hash) VALUES ('qa-monitor','personal',$1,$2,'staging','candidate',3,4,'threshold',0,0,21,22,'gate')",
	} {
		args := []any{p, id}
		if !strings.Contains(q, "$2") {
			args = args[:1]
		}
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version)
		VALUES($1,'personal',$2,$3,6,$4,$5,'review','completed',1)`, attempt, p, dev, base, head); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version)
		VALUES('personal',$1,7,6,1,jsonb_build_object('head',$2::text),1,0)`, p, head); err != nil {
		t.Fatal(err)
	}
	readiness := object{"protocol": 1, "projectId": p}
	status, result := o.req(t, "POST", "/api/v1/device/project-readiness", readiness, token)
	if status != 200 || result["githubConnected"] != true || fmt.Sprint(result["deploymentEnvironments"]) != "[staging]" {
		t.Fatal(status, result)
	}
	readiness["projectId"] = "unassigned"
	if status, _ := o.req(t, "POST", "/api/v1/device/project-readiness", readiness, token); status != 403 {
		t.Fatal(status)
	}
	// The two PNGs exercise the route-specific body limit as well as decoding.
	im := image.NewRGBA(image.Rect(0, 0, 160, 120))
	_, _ = rand.Read(im.Pix)
	for i := 3; i < len(im.Pix); i += 4 {
		im.Pix[i] = 255
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, im); err != nil {
		t.Fatal(err)
	}
	report := protocol.BrowserQAReport{Protocol: 1, AttemptID: attempt, IssueNumber: 6, Assertion: "An order of $50 ships free.", CheckedAt: time.Now().UTC(),
		Before: protocol.BrowserQAObservation{Revision: base, Result: "fail", Observed: "$5 shipping", Screenshot: pngBytes.Bytes()},
		After:  protocol.BrowserQAObservation{Revision: head, Result: "pass", Observed: "Free shipping", Screenshot: pngBytes.Bytes()}}
	path := "projects/" + p + "/incidents/" + id + "/browser-qa"
	o.post(t, path, object{"version": 2, "report": report}, 409)
	invalid := report
	invalid.After.Revision = strings.Repeat("d", 40)
	o.post(t, path, object{"version": 1, "report": invalid}, 409)
	invalid.After.Screenshot = []byte("not a PNG")
	o.post(t, path, object{"version": 1, "report": invalid}, 400)
	o.post(t, path, object{"version": 1, "report": report}, 200)
	o.post(t, path, object{"version": 1, "report": report}, 200)
	for _, path := range []string{path, "projects/" + p + "/pulls/7/browser-qa"} {
		status, result := o.req(t, "GET", "/api/v1/"+path, nil, "")
		v, ok := result["report"].(object)
		if status != 200 || !ok || v["attemptId"] != attempt {
			t.Fatal(status, ok)
		}
	}
	if _, err := s.DB.Exec(`UPDATE pull_observations SET snapshot=jsonb_build_object('head',$1::text) WHERE project_id=$2`, invalid.After.Revision, p); err != nil {
		t.Fatal(err)
	}
	if status, result := o.req(t, "GET", "/api/v1/projects/"+p+"/pulls/7/browser-qa", nil, ""); status != 200 || result["report"] != nil {
		t.Fatalf("expected no report for moved head (HTTP %d)", status)
	}
}
