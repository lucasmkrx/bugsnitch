package planning

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func TestPlannedIssueDriftCannotReviveStaleScope(t *testing.T) {
	dsn := os.Getenv("BUGSNITCH_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("BUGSNITCH_TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("set BUGSNITCH_TEST_DSN")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "plan_drift_" + id()
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	ctx := context.Background()
	db, err := storage.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, e := db.Exec(query, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO projects(id,workspace_id,name,repository)VALUES('p','personal','Disposable','example/test');
	INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id)VALUES('personal','p',31,32);
	INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order)VALUES('old','personal','p',1,'{}','[]'),('reviewed','personal','p',2,'{}','[]');
	INSERT INTO devices(id,workspace_id,name,platform)VALUES('d','personal','test','test');
	INSERT INTO device_projects(workspace_id,device_id,project_id)VALUES('personal','d','p');
	INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version)VALUES('attempt','personal','p','d',17,'base','','implement','running',1)`)
	i := github.Issue{ID: 70, Number: 17, Title: "Shipping boundary", Body: "Include the threshold", State: "open"}
	exec(`INSERT INTO planned_issue_state(workspace_id,project_id,installation_id,repository_id,number,github_id,title,digest,source_state,checked_at,availability) VALUES('personal','p',31,32,17,70,$1,$2,'open',now(),'current')`, i.Title, requirements(i))
	bind := func(plan string) {
		t.Helper()
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e = BindIssues(ctx, tx, "personal", "p", plan, domain.Plan{Tickets: []domain.Ticket{{Number: 17}}}); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	eligible := func(plan string, want bool) {
		t.Helper()
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		e = CheckIssue(ctx, tx, "personal", "p", plan, 17, false)
		if want && e != nil || !want && !errors.Is(e, ErrIssueDrift) {
			t.Fatal("eligibility", plan, e, want)
		}
	}
	bind("old")
	eligible("old", true)
	eligible("", false)
	hook := func(action string, delivery int) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"action": action, "issue": map[string]int{"number": 17, "id": 70}, "installation": map[string]int{"id": 31}, "repository": map[string]int{"id": 32}})
		m := hmac.New(sha256.New, []byte("fixture"))
		m.Write(body)
		req := httptest.NewRequest("POST", "/hooks/github", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("00000000-0000-4000-8000-%012d", delivery))
		req.Header.Set("X-GitHub-Event", "issues")
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m.Sum(nil)))
		r := httptest.NewRecorder()
		(incidents.Store{DB: db}).GitHubWebhook("fixture").ServeHTTP(r, req)
		if r.Code != 202 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	hook("closed", 1)
	hook("reopened", 2)
	hook("reopened", 3)
	var gen, pauses int
	if err = db.QueryRow("SELECT generation,(SELECT count(*) FROM controls WHERE operation='pause') FROM planned_issue_state").Scan(&gen, &pauses); err != nil || gen != 3 || pauses != 1 {
		t.Fatal(gen, pauses, err)
	}
	eligible("old", false)
	read := func(failure error) {
		t.Helper()
		exec("UPDATE planned_issue_state SET next_check=now()")
		if err := (Store{db}).reconcileIssue(ctx, func(context.Context, int64, int64, string, int64) (github.Issue, error) { return i, failure }); err != nil {
			t.Fatal(err)
		}
	}
	read(nil)
	eligible("old", false)
	bind("reviewed")
	eligible("reviewed", true)
	read(errors.New("offline"))
	eligible("reviewed", false)
	read(nil)
	eligible("reviewed", true)
	i.Body = "New scope with different verification"
	read(nil)
	eligible("reviewed", false)
	var ticketCount int
	if err = db.QueryRow("SELECT count(*) FROM planned_issue_state").Scan(&ticketCount); err != nil || ticketCount != 1 {
		t.Fatal("canonical issue duplicated", ticketCount, err)
	}
	read(&github.APIError{Status: 404})
	var reason string
	if err = db.QueryRow("SELECT reason FROM planned_issue_state").Scan(&reason); err != nil || !strings.Contains(reason, "moved") {
		t.Fatal(reason, err)
	}
}
