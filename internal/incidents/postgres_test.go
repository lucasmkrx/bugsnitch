package incidents

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func disposableStore(t *testing.T) Store {
	t.Helper()
	dsn := os.Getenv("BUGSNITCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BUGSNITCH_TEST_DATABASE_URL to a disposable PostgreSQL URL")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_incidents_" + newID()
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := storage.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO projects(id,workspace_id,name,repository) VALUES('p','personal','Fixture','example/disposable');
	INSERT INTO sentry_mappings(workspace_id,project_id,installation_uuid,provider_project) VALUES('personal','p','`+installation+`',21);
	INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal','p',88,99)`)
	return Store{db}
}
func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func one(t *testing.T, s Store) Incident {
	t.Helper()
	is, err := s.List(context.Background(), "personal")
	if err != nil || len(is) != 1 {
		t.Fatalf("incidents: %v %v", len(is), err)
	}
	return is[0]
}
func signedEvent(s Store, b []byte) int {
	return post(s.SentryWebhook("sentry-fixture"), b, map[string]string{"Sentry-Hook-Signature": sign("sentry-fixture", b), "Sentry-Hook-Resource": "error"}).Code
}

type transportFixture func(*http.Request) (*http.Response, error)

func (f transportFixture) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(status int, obj any) *http.Response {
	b, _ := json.Marshal(obj)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}
}
func TestPostgresReplayAndLostIssueResponse(t *testing.T) {
	s := disposableStore(t)
	ctx := context.Background()
	b := errorPayload(eventOne, "v1", "production", time.Now())
	if got := signedEvent(s, []byte(strings.Replace(string(b), installation, "dc9783eb-e04b-4479-b167-42a4403d9590", 1))); got != 403 {
		t.Fatalf("wrong installation got %d", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := signedEvent(s, b); got != 202 {
				t.Errorf("duplicate HTTP %d", got)
			}
		}()
	}
	wg.Wait()
	if i, err := s.List(ctx, "personal"); err != nil || len(i) > 0 {
		t.Fatal("acceptance must precede processing", err)
	}
	if err := s.processOne(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.processOne(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	inc := one(t, s)
	if inc.Occurrences != 1 || inc.Source != "sentry" {
		t.Fatal(inc)
	}
	var sanitized string
	if err := s.DB.QueryRow("SELECT row_to_json(b)::text FROM incident_inbox b").Scan(&sanitized); err != nil || strings.Contains(sanitized, markerSecret) {
		t.Fatal("inbox contained unapproved data", err)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Promote(ctx, "personal", "p", inc.ID, inc.Version); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := s.Promote(ctx, "another-workspace", "p", inc.ID, inc.Version); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace promotion %v", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "fixture.pem")
	if err = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := github.NewApp("fixture", file)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	writes := 0
	outage := true
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = transportFixture(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.github.com" || req.Header.Get("X-GitHub-Api-Version") != github.APIVersion {
			t.Error("invalid HTTP contract")
		}
		if outage {
			return nil, errors.New("fixture outage")
		}
		switch req.URL.Path {
		case "/app":
			return jsonResponse(200, map[string]any{"id": 50, "slug": "sample-bug-app"}), nil
		case "/app/installations/88/access_tokens":
			var data struct {
				Repositories []int64 `json:"repository_ids"`
				Permissions  map[string]string
			}
			if json.NewDecoder(req.Body).Decode(&data) != nil || len(data.Repositories) != 1 || data.Repositories[0] != 99 || len(data.Permissions) != 1 || data.Permissions["issues"] != "write" {
				t.Error("token not scoped to mapped repository and permissions")
			}
			return jsonResponse(201, map[string]string{"token": "fixture-installation-token"}), nil
		case "/repositories/99":
			return jsonResponse(200, map[string]any{"id": 99, "full_name": "example/disposable"}), nil
		case "/repos/example/disposable/issues":
			if req.Method == "POST" {
				writes++
				var draft map[string]string
				if json.NewDecoder(req.Body).Decode(&draft) != nil {
					t.Error("unreadable issue")
				}
				body = draft["body"]
				if strings.Contains(body, markerSecret) || !strings.Contains(body, "TypeError") {
					t.Error("issue evidence invalid")
				}
				// The external effect occurred. Its response did not reach Bugsnitch.
				return nil, errors.New("fixture response lost")
			}
			return jsonResponse(200, []map[string]any{{"number": 7, "id": 55, "title": "repaired issue", "body": body, "state": "open", "user": map[string]string{"login": "sample-bug-app[bot]", "type": "Bot"}}}), nil
		case "/repos/example/disposable/issues/7":
			return jsonResponse(200, map[string]any{"number": 7, "id": 55, "title": "changed externally", "body": body, "state": "closed"}), nil
		}
		t.Errorf("unexpected fixture request %v", req.URL)
		return nil, errors.New("unexpected request")
	})
	work := func() {
		t.Helper()
		mustExec(t, s.DB, "UPDATE incident_issues SET next_run=now()")
		if err := s.syncOne(ctx, app); err != nil {
			t.Fatal(err)
		}
	}
	work()
	if got := one(t, s); got.Promotion == nil || *got.Promotion != "queued" || writes != 0 {
		t.Fatal("outage dropped intent", got)
	}
	outage = false
	work()
	if got := one(t, s); *got.Promotion != "reconciling" || writes != 1 {
		t.Fatal("lost reply incorrectly classified", got)
	}
	work()
	if got := one(t, s); got.IssueNumber == nil || *got.IssueNumber != 7 || writes != 1 {
		t.Fatal("ambiguous write duplicated or lost", got)
	}
	hook := func(id, event, payload string) int {
		b := []byte(payload)
		return post(s.GitHubWebhook("github-fixture"), b, map[string]string{"X-GitHub-Delivery": id, "X-GitHub-Event": event, "X-Hub-Signature-256": "sha256=" + sign("github-fixture", b)}).Code
	}
	opened := `{"action":"opened","repository":{"id":99},"installation":{"id":88},"issue":{"id":55,"number":7}}`
	for i := 0; i < 2; i++ {
		if got := hook(installation, "issues", opened); got != 202 {
			t.Fatalf("replay hint %d", got)
		}
	}
	if err := s.syncOne(ctx, app); err != nil {
		t.Fatal(err)
	}
	if got := one(t, s); got.IssueState == nil || *got.IssueState != "closed" || writes != 1 {
		t.Fatal("stale webhook overwrote current authority", got)
	}
	if got := hook("605e0fad-f2cc-4fa8-9ff1-36a35a765b64", "installation", `{"action":"suspend","installation":{"id":88}}`); got != 202 {
		t.Fatal(got)
	}
	if got := hook("5ceb56cc-525c-46a6-ad8d-f3f40b61f066", "issues", opened); got != 403 {
		t.Fatal("revoked mapping accepted a delivery", got)
	}
}
func TestPostgresReceiptsAndRecurrence(t *testing.T) {
	s := disposableStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	t0 := now.Add(-4 * time.Hour)
	add := func(id, release string, at time.Time) {
		t.Helper()
		if got := signedEvent(s, errorPayload(id, release, "production", at)); got != 202 {
			t.Fatal(got)
		}
		if err := s.processOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	add(eventOne, "v1", t0)
	fix := strings.Repeat("a", 40)
	put := func(kind, build, candidate string, at time.Time, positive, coverage *bool) {
		t.Helper()
		inc := one(t, s)
		r := Receipt{CommandID: fmt.Sprintf("command-%d", inc.Version), Version: inc.Version, Kind: kind, Build: build, Candidate: candidate, At: at, Evidence: "Operator checked the affected checkout flow", Positive: positive, Coverage: coverage}
		if kind != "fix" {
			r.Environment = "production"
		}
		if kind == "deployment" {
			r.WindowSeconds = 1800
		}
		if err := s.receipt(ctx, "personal", "p", inc.ID, r); err != nil {
			t.Fatal(err)
		}
		if err := s.receipt(ctx, "personal", "p", inc.ID, r); err != nil {
			t.Fatal("idempotent receipt failed", err)
		}
		r.Evidence = "Different evidence, same command"
		if !errors.Is(s.receipt(ctx, "personal", "p", inc.ID, r), ErrConflict) {
			t.Fatal("conflicting receipt accepted")
		}
	}
	expect := func(want string) {
		t.Helper()
		v, err := s.verdict(ctx, "personal", "p", one(t, s).ID, "production")
		if err != nil || v.Status != want {
			t.Fatalf("verdict=%v err=%v want=%s", v.Status, err, want)
		}
	}
	yes := true
	put("fix", "", fix, t0.Add(time.Minute), nil, nil)
	expect("Awaiting deployment")
	put("deployment", "v2", fix, t0.Add(time.Hour), nil, nil)
	expect("Verification inconclusive")
	put("validation", "v2", fix, t0.Add(130*time.Minute), &yes, nil)
	expect("Coverage unknown")
	put("validation", "v2", fix, t0.Add(131*time.Minute), &yes, &yes)
	expect("Verified in environment")
	add("223456789abcdef0123456789abcdef0", "v1", t0.Add(3*time.Hour))
	expect("Verified in environment")
	add("323456789abcdef0123456789abcdef0", "v2", t0.Add(3*time.Hour))
	expect("Needs triage")
	put("deployment", "v1", strings.Repeat("b", 40), now.Add(-time.Minute), nil, nil)
	expect("Awaiting deployment")
}
