// SPDX-License-Identifier: MPL-2.0
package planning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeIssues struct {
	bodies    []string
	last      github.Issue
	rejection *github.APIError
}

func (*fakeIssues) Close() {}

func (f *fakeIssues) Create(_ context.Context, title, body string) (github.Issue, error) {
	if f.rejection != nil {
		return github.Issue{}, f.rejection
	}
	f.bodies = append(f.bodies, body)
	n := int64(len(f.bodies) + 40)
	f.last = github.Issue{ID: n + 200, Number: n, State: "open", Title: title, Body: body}
	if n == 41 {
		return github.Issue{}, errors.New("response lost after durable create")
	}
	return f.last, nil
}
func (f *fakeIssues) Find(_ context.Context, marker string, _ time.Time) (*github.Issue, error) {
	if strings.HasSuffix(f.last.Body, "\n"+marker) {
		return &f.last, nil
	}
	return nil, nil
}
func TestOrderedPublicationLostResponse(t *testing.T) {
	dsn := os.Getenv("BUGSNITCH_TEST_DSN")
	if dsn == "" {
		t.Skip("set BUGSNITCH_TEST_DSN")
	}
	ctx := context.Background()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := "plan_" + id()
	if _, err = root.Exec("CREATE SCHEMA " + name); err != nil {
		t.Fatal(err)
	}
	defer root.Exec("DROP SCHEMA " + name + " CASCADE")
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	db, err := storage.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("INSERT INTO projects(id,workspace_id,name,repository) VALUES('p','personal','Disposable','example/fixture'); INSERT INTO github_mappings(workspace_id,project_id,repository_id,installation_id) VALUES('personal','p',55,66)"); err != nil {
		t.Fatal(err)
	}
	a := domain.Ticket{Title: "Export", Number: 7, Criteria: []string{"Opens"}, EstimateHours: 1, Verification: "Run tests"}
	b := a
	b.Number = 2
	b.Dependencies = []int{7}
	b.Title = "View export"
	doc := domain.Plan{Title: "Publish", Milestone: "V1", Tickets: []domain.Ticket{b, a}}
	s := Store{db}
	pub, err := s.Propose(ctx, "personal", "p", "stable_publication", 1, doc)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Propose(ctx, "personal", "p", "stable_publication", 1, doc)
	if err != nil || replay != pub {
		t.Fatal("duplicate proposal", replay, err)
	}
	if err = s.Approve(ctx, "personal", pub, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.Approve(ctx, "personal", pub, 1); err != nil {
		t.Fatal(err)
	}
	fake := new(fakeIssues)
	open := func(context.Context, int64, int64, string) (issueWriter, error) { return fake, nil }
	for _, rejection := range []*github.APIError{{Status: 429, RetryAfter: 10 * time.Minute}, {Status: 403}} {
		fake.rejection = rejection
		var before time.Time
		if err = db.QueryRow("SELECT now()").Scan(&before); err != nil {
			t.Fatal(err)
		}
		if err = s.step(ctx, open); err != nil {
			t.Fatal(err)
		}
		var deferred bool
		earliest := before.Add(max(time.Minute, rejection.RetryAfter))
		if err = db.QueryRow("SELECT status='queued' AND next_run>=$2 FROM planned_issues WHERE publication_id=$1 AND local_number=7", pub, earliest).Scan(&deferred); err != nil || !deferred {
			t.Fatalf("HTTP %d did not schedule another creation after backoff: %v", rejection.Status, err)
		}
		if _, err = db.Exec("UPDATE planned_issues SET next_run=now()"); err != nil {
			t.Fatal(err)
		}
	}
	fake.rejection = nil
	if err = s.step(ctx, open); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE planned_issues SET next_run=now()"); err != nil {
		t.Fatal(err)
	}
	if err = s.step(ctx, open); err != nil {
		t.Fatal(err)
	}
	if err = s.step(ctx, open); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var status string
	var approved bool
	if err = db.QueryRow("SELECT x.status,v.document,v.approved_at IS NOT NULL FROM publications x JOIN plan_versions v ON v.id=x.plan_id WHERE x.id=$1", pub).Scan(&status, &raw, &approved); err != nil {
		t.Fatal(err)
	}
	var result domain.Plan
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if status != "published" || approved || len(fake.bodies) != 2 || !strings.Contains(fake.bodies[1], "Depends on #41") || result.Tickets[0].Number != 42 || result.Tickets[0].Dependencies[0] != 41 {
		t.Fatal("wrong canonical publication", status, result.Tickets)
	}
}
