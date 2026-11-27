// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

type boardFixture struct {
	t         *testing.T
	calls     []string
	nextError error
}

func (f *boardFixture) SetProjectStatus(_ context.Context, install, repo int64, name string, issue, githubID int64, board, item, option string) error {
	f.t.Helper()
	if install != 22 || repo != 31 || name != "example/web" || issue != 7 || githubID != 47 || board != "board" || item != "item" {
		f.t.Fatalf("unexpected board status subject: %d %d %s %d %d %s %s", install, repo, name, issue, githubID, board, item)
	}
	f.calls = append(f.calls, option)
	err := f.nextError
	f.nextError = nil
	return err
}

func TestBoardStatusFollowsPickupAndVerifiedHandover(t *testing.T) {
	dsn := os.Getenv("BUGSNITCH_TEST_DSN")
	if dsn == "" {
		t.Skip("set BUGSNITCH_TEST_DSN to a disposable PostgreSQL database")
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
	schema := "test_board_" + rand.Text()
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	ctx := t.Context()
	db, err := storage.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects(id,workspace_id,name,repository) VALUES ('p','personal','Example','example/web');
		INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES ('personal','p',22,31);
		INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id)
		  VALUES('personal','p',7,47,22,31);
		INSERT INTO github_project_tickets(workspace_id,project_id,number,project_node,item_node,board_number,github_id)
		  VALUES ('personal','p',7,'board','item',3,47);
		INSERT INTO devices(id,workspace_id,name,platform) VALUES('d','personal','test','test');
		INSERT INTO device_projects(workspace_id,device_id,project_id) VALUES ('personal','d','p');
		INSERT INTO worker_starts(id,workspace_id,project_id,device_id,issue_number,request_id,requested_plan,policy_version,github_id,digest,revision,title,status)
		  VALUES('other','personal','p','d',8,'other','',1,48,'digest','now','Other issue','queued'),
		      ('stale','personal','p','d',7,'stale','',1,99,'digest','now','Replaced issue','queued')`)
	fake := &boardFixture{t: t}
	sync := func(want bool) {
		t.Helper()
		processed, err := syncBoardStatus(ctx, db, fake, "Working", "Delivered")
		if err != nil || processed != want {
			t.Fatalf("sync processed=%v want=%v err=%v", processed, want, err)
		}
	}
	sync(false)
	exec("UPDATE worker_starts SET status='planning'")
	sync(false)
	exec(`UPDATE worker_starts SET status='failed';
		INSERT INTO worker_starts(id,workspace_id,project_id,device_id,issue_number,request_id,requested_plan,policy_version,github_id,digest,revision,title,status)
		VALUES('current','personal','p','d',7,'current','',1,47,'digest','now','Issue','queued')`)
	sync(false)
	exec("UPDATE worker_starts SET status='planning' WHERE id='current'")
	fake.nextError = &github.APIError{Status: 429, RetryAfter: 3 * time.Minute}
	sync(true)
	var backedOff bool
	err = db.QueryRow(`SELECT desired='active' AND applied IS NULL AND failures=1 AND reason<>''
		AND next_run > now()+interval '170 seconds' FROM github_project_status`).Scan(&backedOff)
	if err != nil || !backedOff {
		t.Fatal("pending retry", backedOff, err)
	}
	sync(false)
	exec("UPDATE github_project_status SET next_run=now()")
	exec("UPDATE github_mappings SET active=false")
	sync(false)
	exec("UPDATE github_mappings SET active=true")
	sync(true)
	sync(false)
	exec(`INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version)
		VALUES('personal','p',21,7,2,'{"state":"OPEN","mergedSha":"merge","target":"main"}',1,0),
		      ('personal','p',22,8,2,'{"state":"MERGED","mergedSha":"merge","target":"main"}',1,0)`)
	handover := func(pull, version int, sha, target, build string) {
		t.Helper()
		exec(`INSERT INTO pull_handovers(workspace_id,project_id,pull_number,inspection_version,merged_sha,target,environment,build,evidence_digest,summary,test_steps,actor_role,actor_id)
			VALUES ('personal','p',$1,$2,$3,$4,'production',$5,'receipt','summary','tests','owner','owner')`, pull, version, sha, target, build)
	}
	handover(22, 2, "merge", "main", "other-issue")
	handover(21, 2, "merge", "main", "still-open")
	exec(`UPDATE pull_observations SET snapshot='{"state":"MERGED","mergedSha":"merge","target":"main"}' WHERE number=21`)
	handover(21, 1, "merge", "main", "old-inspection")
	handover(21, 2, "other", "main", "wrong-merge")
	handover(21, 2, "merge", "other", "wrong-target")
	sync(false)
	handover(21, 2, "merge", "main", "verified")
	sync(true)
	handover(21, 2, "merge", "main", "second-build")
	sync(false)
	var applied string
	var failures int
	if err = db.QueryRow("SELECT applied,failures FROM github_project_status").Scan(&applied, &failures); err != nil || applied != "done" || failures != 0 {
		t.Fatal(applied, failures, err)
	}
	if !slices.Equal(fake.calls, []string{"Working", "Working", "Delivered"}) {
		t.Fatal(fake.calls)
	}
}
