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

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func TestHealthyHeadCannotAuthorizeUnverifiedCombination(t *testing.T) {
	db, _ := scratchDB(t)
	s := Store{DB: db}
	obs := github.PullObservation{Checks: "SUCCESS", CheckCount: 1, Review: "APPROVED", IssueState: "OPEN", State: "OPEN", Base: "base"}
	p, err := s.save(context.Background(), "personal", "p", 4, 3, 0, 10, 11, "example/test", obs)
	if err != nil {
		t.Fatal(err)
	}
	if p.AutoMerge || !strings.Contains(strings.Join(p.Blockers, " "), "Combined validation evidence is missing") {
		t.Fatal(p)
	}
}
func scratchDB(t *testing.T) (*sql.DB, func(string, ...any)) {
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
	t.Cleanup(func() { admin.Close() })
	var b [12]byte
	rand.Read(b[:])
	schema := fmt.Sprintf("test_pull_%x", b)
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	ctx := context.Background()
	db, err := storage.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO projects(id,workspace_id,name,repository) VALUES('p','personal','Fixture','example/test');INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal','p',10,11)`)
	return db, run
}
func TestPostgresInvalidateEvidenceOnSubjectChange(t *testing.T) {
	ctx := context.Background()
	db, run := scratchDB(t)
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
	for _, change := range []func(){func() { obs.Head = strings.Repeat("f", 40) }, func() { obs.Base = strings.Repeat("e", 40) }, func() { obs.Criteria = "v2" }, func() { run("UPDATE projects SET version=version+1,policy_version=policy_version+1 WHERE id='p'") }} {
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

func TestQueueAndPassingChecksCannotReplaceDecisionForTarget(t *testing.T) {
	db, run := scratchDB(t)
	s := Store{DB: db}
	observed := github.PullObservation{Queue: &github.QueueObservation{OwnerType: "Organization", Present: true, Rules: &github.RuleSnapshot{QueueRequired: true, RequiredChecks: 2, AppBoundChecks: 2}}, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), Target: "disposable", State: "OPEN", IssueState: "OPEN", Review: "APPROVED", Checks: "SUCCESS", CheckCount: 2}
	for i, target := range []string{"disposable", "production"} {
		observed.Target = target
		number := int64(i + 4)
		p, err := s.save(context.Background(), "personal", "p", number, 3, 0, 10, 11, "example/test", observed)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"combined_validation", "independent_review"} {
			run("INSERT INTO pull_evidence(workspace_id,project_id,number,version,kind,reference) VALUES('personal','p',$1,$2,$3,'test-artifact')", number, p.Version, kind)
		}
		p, err = s.save(context.Background(), "personal", "p", number, 3, p.Version, 10, 11, "example/test", observed)
		if err != nil || p.Validation == "" || p.IndependentReview == "" {
			t.Fatal(err, p)
		}
		if p.AutoMerge || !strings.Contains(strings.Join(p.Blockers, " "), "approved target") {
			t.Fatalf("accepted a queue without a target-bound decision for %s", target)
		}
	}
}
