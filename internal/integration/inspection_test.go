package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func TestHealthyHeadCannotAuthorizeUnverifiedCombination(t *testing.T) {
	p := Pull{PullObservation: github.PullObservation{Checks: "SUCCESS", CheckCount: 1, Review: "APPROVED", IssueState: "OPEN", State: "OPEN", Base: "base"}, ObservedAt: time.Now()}
	assess(&p, flags{active: true, current: true}, time.Now())
	if p.AutoMerge || !strings.Contains(strings.Join(p.Blockers, " "), "Combined validation evidence is missing") {
		t.Fatal(p)
	}
}
func TestPostgresInvalidateEvidenceOnSubjectChange(t *testing.T) {
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
	defer admin.Close()
	var b [12]byte
	rand.Read(b[:])
	schema := fmt.Sprintf("test_pull_%x", b)
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
	run := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO projects(id,workspace_id,name,repository) VALUES('p','personal','Fixture','example/test');INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal','p',10,11)`)
	s := Store{DB: db}
	obs := github.PullObservation{State: "OPEN", IssueState: "OPEN", Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), Target: "main", Criteria: "v1", Review: "APPROVED", Checks: "SUCCESS", CheckCount: 2}
	var version int64
	save := func() Pull {
		t.Helper()
		p, err := s.save(ctx, "personal", "p", 4, 3, version, 10, 11, "example/test", obs)
		if err != nil {
			t.Fatal(err)
		}
		version = p.Version
		return p
	}
	save()
	for _, change := range []func(){func() { obs.Head = strings.Repeat("f", 40) }, func() { obs.Base = strings.Repeat("e", 40) }, func() { obs.Criteria = "v2" }, func() { run("UPDATE projects SET version=version+1 WHERE id='p'") }} {
		run(`INSERT INTO pull_evidence(workspace_id,project_id,number,version,kind,reference) VALUES('personal','p',4,$1,'combined_validation',$2)`, version, strings.Repeat("5", 64))
		if p := save(); p.Validation == "" {
			t.Fatal("matching evidence missing")
		}
		before := version
		change()
		p := save()
		if p.Version != before+1 || p.Validation != "" || p.AutoMerge {
			t.Fatal("stale evidence survived subject change", p)
		}
		if _, err := s.save(ctx, "personal", "p", 4, 3, before, 10, 11, "example/test", obs); !errors.Is(err, ErrConflict) {
			t.Fatal("stale refresh was accepted", err)
		}
	}
	mine, err := s.List(ctx, "elsewhere")
	if err != nil || len(mine) != 0 {
		t.Fatal("cross-workspace leak", err)
	}
}
