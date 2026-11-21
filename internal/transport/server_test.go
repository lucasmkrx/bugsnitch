// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func fixture(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("BUGSNITCH_TEST_DSN")
	if dsn == "" {
		t.Skip("set BUGSNITCH_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + random(8)
	if _, err = db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE"); db.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped, err := storage.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { scoped.Close() })
	if err = storage.Migrate(ctx, scoped); err != nil {
		t.Fatal(err)
	}
	return &Server{DB: scoped, Incidents: incidents.Store{DB: scoped}, Origin: "http://localhost:8787", OwnerToken: random(32), Plugin: PluginAuth{LocalToken: random(32)}}
}

type system struct {
	handler http.Handler
	cookie  *http.Cookie
}

func (c system) req(t *testing.T, method, path string, body any, token string) (int, object) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	obj := object{}
	if err = json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatalf("non-JSON %s %s: %d", method, path, w.Code)
	}
	return w.Code, obj
}
func (c system) post(t *testing.T, path string, input any, status int) object {
	t.Helper()
	got, o := c.req(t, "POST", "/api/v1/"+path, input, "")
	if got != status {
		t.Fatalf("%s: got %d, want %d: %v", path, got, status, o)
	}
	return o
}
func login(t *testing.T, s *Server) system {
	h := s.Handler()
	r := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(fmt.Sprintf(`{"token":%q}`, s.OwnerToken)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return system{h, w.Result().Cookies()[0]}
}
func TestOwnerDecisionAndCrossInterfaceState(t *testing.T) {
	s := fixture(t)
	c := login(t, s)
	p := c.post(t, "projects", object{"name": "Atlas", "repository": "example/atlas"}, 201)["id"].(string)
	if code, _ := (system{handler: c.handler}).req(t, "GET", "/api/v1/state", nil, ""); code != 401 {
		t.Fatal(code)
	}
	c.post(t, "projects/"+p+"/dispatch", object{"paused": true, "version": 1}, 200)
	d := c.post(t, "projects/"+p+"/dispatch", object{"paused": false, "version": 2}, 202)["decisionId"].(string)
	answer := object{"answer": "Resume dispatch", "version": 1, "requestId": "choice_12345"}
	c.post(t, "decisions/"+d+"/answer", answer, 200)
	c.post(t, "decisions/"+d+"/answer", answer, 200)
	answer["answer"] = "Keep paused"
	c.post(t, "decisions/"+d+"/answer", answer, 409)
	c.post(t, "projects/"+p+"/dispatch", object{"paused": true, "version": 3}, 200)
	d = c.post(t, "projects/"+p+"/dispatch", object{"paused": false, "version": 4}, 202)["decisionId"].(string)
	if _, err := s.DB.Exec("UPDATE projects SET version=version+1 WHERE id=$1", p); err != nil {
		t.Fatal(err)
	}
	c.post(t, "decisions/"+d+"/answer", object{"answer": "Resume dispatch", "version": 1, "requestId": "choice_23456"}, 409)
	if _, err := s.DB.Exec("INSERT INTO workspaces VALUES('foreign','owner','Other',now()); INSERT INTO projects(id,workspace_id,name,repository) VALUES('hidden','foreign','Invisible','example/hidden')"); err != nil {
		t.Fatal(err)
	}
	c.post(t, "projects/hidden/dispatch", object{"paused": true, "version": 1}, 404)
	request := func(name string, args object) []byte {
		body, _ := json.Marshal(object{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": object{"name": name, "arguments": args}})
		r := httptest.NewRequest("POST", "http://localhost:8787/mcp", bytes.NewReader(body))
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("MCP-Protocol-Version", "2025-11-25")
		r.Header.Set("Authorization", "Bearer "+s.Plugin.LocalToken)
		w := httptest.NewRecorder()
		c.handler.ServeHTTP(w, r)
		b, _ := io.ReadAll(w.Body)
		if w.Code != 200 || bytes.Contains(b, []byte(`"isError":true`)) {
			t.Fatalf("MCP failed %d %s", w.Code, b)
		}
		return b
	}
	if raw := string(request("list_projects", object{})); !strings.Contains(raw, "Atlas") || strings.Contains(raw, "Invisible") {
		t.Fatal(raw)
	}
	request("report_incident", object{"projectId": p, "observed": "Blank preview\nAuthorization: Bearer SYNTHETICMARKER", "expected": "A preview", "steps": "Open the preview", "requestId": "reported_001"})
	_, state := c.req(t, "GET", "/api/v1/state", nil, "")
	raw, _ := json.Marshal(state)
	if !bytes.Contains(raw, []byte("Blank preview")) || bytes.Contains(raw, []byte("SYNTHETICMARKER")) || bytes.Contains(raw, []byte("Invisible")) {
		t.Fatal("state did not preserve authorization and redaction")
	}
}
func TestDeviceReplayRevocationAndBinding(t *testing.T) {
	s := fixture(t)
	c := login(t, s)
	p := c.post(t, "projects", object{"name": "Atlas", "repository": "example/atlas"}, 201)["id"].(string)
	pair := func(name string) (string, string) {
		code := c.post(t, "pairings", object{"name": name, "projectIds": []string{p}}, 201)["code"].(string)
		token := random(32)
		b := protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}
		id := c.post(t, "device/pair", b, 200)["deviceId"].(string)
		if c.post(t, "device/pair", b, 200)["deviceId"] != id {
			t.Fatal("lost acknowledgment duplicated enrollment")
		}
		b.Credential = random(32)
		c.post(t, "device/pair", b, 403)
		return token, id
	}
	key, device := pair("one")
	other, _ := pair("two")
	a := protocol.Attempt{ProjectID: p, ID: "attempt_001", BaseSHA: strings.Repeat("a", 40), IssueNumber: 8, Phase: "implement", State: "running", Version: 3}
	code, _ := c.req(t, "POST", "/api/v1/device/admission", protocol.Admission{Protocol: 1, ProjectID: p, AttemptID: a.ID, IssueNumber: a.IssueNumber, BaseSHA: a.BaseSHA, Operation: "admit", Sequence: 1}, key)
	if code != 200 {
		t.Fatal(code)
	}
	syncReq := func(a protocol.Attempt, k string, want int) {
		code, o := c.req(t, "POST", "/api/v1/device/sync", protocol.Sync{Protocol: 1, Attempts: []protocol.Attempt{a}}, k)
		if code != want {
			t.Errorf("sync = %d, want %d, %v", code, want, o)
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { syncReq(a, key, 200) })
	}
	wg.Wait()
	a.Version = 2
	a.Phase = "plan"
	syncReq(a, key, 200)
	syncReq(a, other, 403)
	a.ProjectID = "hidden"
	syncReq(a, key, 403)
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM events WHERE kind='attempt.reported'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("events %d %v", n, err)
	}
	if _, err := s.DB.Exec("UPDATE devices SET last_contact=now()-interval '1 hour' WHERE id=$1", device); err != nil {
		t.Fatal(err)
	}
	_, state := c.req(t, "GET", "/api/v1/state", nil, "")
	if !strings.Contains(fmt.Sprint(state["attempts"]), "disconnected") {
		t.Fatal(state["attempts"])
	}
	c.post(t, "devices/"+device+"/revoke", object{}, 200)
	c.post(t, "devices/"+device+"/revoke", object{}, 200)
	a.ProjectID = p
	syncReq(a, key, 403)
}
func TestOriginAndCredentials(t *testing.T) {
	s := Server{Origin: "http://localhost:8787"}
	r := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://elsewhere.example")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, raw := range []string{"http://outside.example", "https://alice@example.test", "file:///tmp/socket", "http://localhost.evil.test", "http://localhost?query"} {
		if ValidOrigin(raw) {
			t.Error(raw)
		}
	}
}

func TestCompetingLocalClaimsAndPlanGates(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	p := owner.post(t, "projects", object{"name": "Example", "repository": "example/atlas"}, 201)["id"].(string)
	criteria := []string{"The exported file opens."}
	ticket := func(n int, deps []int, resources []string) object {
		return object{"number": n, "title": fmt.Sprint("Task ", n), "criteria": criteria, "priority": 1, "estimateHours": 1, "verification": "Run the export regression suite", "dependencies": deps, "resources": resources}
	}
	plan := object{"title": "Export", "milestone": "First release", "tickets": []object{ticket(1, nil, []string{"database"}), ticket(2, []int{1}, nil), ticket(3, nil, []string{"database"}), ticket(4, nil, nil)}}
	pv := owner.post(t, "plans", object{"projectId": p, "version": 1, "plan": plan}, 201)["id"].(string)
	owner.post(t, "plans/"+pv+"/approve", object{"version": 2}, 200)
	if _, err := s.DB.Exec("UPDATE workspaces SET ticket_limit=1,turn_limit=1"); err != nil {
		t.Fatal(err)
	}
	pair := func() string {
		code := owner.post(t, "pairings", object{"name": "Computer", "projectIds": []string{p}}, 201)["code"].(string)
		token := random(32)
		owner.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "linux/amd64"}, 200)
		return token
	}
	first, second := pair(), pair()
	send := func(tk string, issue int, op string, seq int64, outcome string) int {
		a := protocol.Admission{Protocol: 1, ProjectID: p, IssueNumber: issue, AttemptID: fmt.Sprintf("attempt_%d", issue), BaseSHA: strings.Repeat("1", 40), Operation: op, Sequence: seq, State: outcome, CriteriaHash: domain.CriteriaHash(criteria)}
		status, _ := owner.req(t, "POST", "/api/v1/device/admission", a, tk)
		return status
	}
	if v := send(second, 2, "admit", 1, ""); v != 409 {
		t.Fatal("unfinished dependency", v)
	}
	type result struct {
		number, status int
		token          string
	}
	results := make(chan result, 2)
	go func() { results <- result{1, send(first, 1, "admit", 1, ""), first} }()
	go func() { results <- result{3, send(second, 3, "admit", 1, ""), second} }()
	win, lose := <-results, <-results
	if win.status != 200 {
		win, lose = lose, win
	}
	if win.status != 200 || lose.status != 409 {
		t.Fatalf("race results %d/%d", win.status, lose.status)
	}
	if send(win.token, win.number, "admit", 1, "") != 200 || send(win.token, win.number, "turn_start", 2, "") != 200 || send(win.token, win.number, "finish", 3, "waiting") != 409 {
		t.Fatal("turn evidence or replay gate")
	}
	if _, err := s.DB.Exec("UPDATE devices SET last_contact=now()-interval '2 hours';UPDATE workspaces SET ticket_limit=2"); err != nil {
		t.Fatal(err)
	}
	if send(lose.token, lose.number, "admit", 1, "") != 409 {
		t.Fatal("shared lock lost on disconnect")
	}
	if send(lose.token, 4, "admit", 1, "") != 200 || send(lose.token, 4, "turn_start", 2, "") != 409 {
		t.Fatal("turn permit lost on disconnect")
	}
	if send(win.token, win.number, "turn_end", 3, "") != 200 || send(win.token, win.number, "finish", 4, "waiting") != 200 || send(lose.token, lose.number, "admit", 1, "") != 409 {
		t.Fatal("waiting lost ownership or locks")
	}
	if send(lose.token, 4, "turn_start", 2, "") != 200 {
		t.Fatal("turn release")
	}
	if send(win.token, win.number, "admit", 5, "") != 200 || send(win.token, win.number, "finish", 6, "completed") != 200 {
		t.Fatal("cannot resume original machine")
	}
	owner.post(t, "plans/"+pv+"/evidence", object{"number": 1, "revision": strings.Repeat("e", 40), "summary": "Owner exercised export against the integrated revision."}, 201)
	if send(first, 2, "admit", 1, "") != 200 {
		t.Fatal("accepted prerequisite did not unblock dependency")
	}
}

func TestPolicyChangeFencesScope(t *testing.T) {
	s := fixture(t)
	c := login(t, s)
	p := c.post(t, "projects", object{"name": "Policy", "repository": "example/policy"}, 201)["id"].(string)
	c.post(t, "projects/"+p+"/policy", object{"version": 1, "preset": "Assisted"}, 200)
	c.post(t, "projects/"+p+"/policy", object{"version": 1, "preset": "Autonomous within limits"}, 409)
	c.post(t, "limits", object{"version": 1, "turns": 1, "tickets": 3}, 200)
	c.post(t, "limits", object{"version": 1, "turns": 2, "tickets": 4}, 409)
	code := c.post(t, "pairings", object{"name": "Computer", "projectIds": []string{p}}, 201)["code"].(string)
	token := random(32)
	c.post(t, "device/pair", protocol.Pair{Protocol: 1, Code: code, Credential: token, Platform: "darwin/arm64"}, 200)
	got, _ := c.req(t, "POST", "/api/v1/device/admission", protocol.Admission{Protocol: 1, ProjectID: p, AttemptID: "attempt_gated", IssueNumber: 1, BaseSHA: strings.Repeat("a", 40), Operation: "admit", Sequence: 1}, token)
	if got != 409 {
		t.Fatal("assisted project admitted unplanned work", got)
	}
}
