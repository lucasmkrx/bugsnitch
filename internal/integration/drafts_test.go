package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
	"github.com/lucasmkrx/bugsnitch/internal/testgithub"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

func TestPostgresDraftReconcilesLostResponseAndRejectsStalePolicy(t *testing.T) {
	db, exec := scratchDB(t)
	ctx := context.Background()
	id := strings.Repeat("1", 32)
	second := strings.Repeat("2", 32)
	head := strings.Repeat("a", 40)
	base := strings.Repeat("b", 40)
	const updated = "2026-10-20T12:00:00Z"
	exec(`INSERT INTO devices(id,workspace_id,name,platform) VALUES('d','personal','fixture','test');INSERT INTO device_projects(workspace_id,device_id,project_id) VALUES('personal','d','p')`)
	for _, a := range []string{id, second} {
		stages, _ := json.Marshal([]map[string]any{{"phase": "validate", "candidateSha": head, "exitCode": 0}, {"phase": "review", "candidateSha": head, "importantFindings": 0}})
		exec(`INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version,evidence,issue_updated_at)
		VALUES($1,'personal','p','d',17,$2,$3,'wrap_up','completed',5,$4,$5)`, a, base, head, string(stages), updated)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "key.pem")
	if err = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	request := DraftRequest{Version: 5, PolicyVersion: 1, Target: "main", Summary: "Fix threshold handling", Behavior: "Qualifying orders have free shipping"}
	var saved string
	var writes atomic.Int32
	origin := testgithub.Serve(t, func(r *http.Request) (*http.Response, error) {
		var obj any
		status := 200
		switch r.URL.Path {
		case "/app":
			obj = map[string]any{"id": 9, "slug": "bugs-fixture"}
		case "/app/installations/10/access_tokens":
			var body struct {
				Permissions map[string]string
				IDs         []int64 `json:"repository_ids"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.IDs) != 1 || body.IDs[0] != 11 || body.Permissions["contents"] != "read" || body.Permissions["pull_requests"] != "write" {
				t.Fatal("invalid token scope")
			}
			obj = map[string]string{"token": "fixture"}
			status = 201
		case "/repositories/11":
			obj = map[string]any{"id": 11, "full_name": "example/test"}
		case "/repos/example/test/issues/17":
			obj = map[string]any{"id": 177, "number": 17, "state": "open", "updated_at": updated}
		case "/repos/example/test/git/ref/heads/main":
			obj = map[string]any{"ref": "refs/heads/main", "object": map[string]string{"type": "commit", "sha": base}}
		case "/repos/example/test/git/ref/heads/bugsnitch/" + id:
			obj = map[string]any{"ref": "refs/heads/bugsnitch/" + id, "object": map[string]string{"type": "commit", "sha": head}}
		case "/repos/example/test/pulls":
			if r.Method == "POST" {
				var payload struct {
					Title, Body, Head, Base string
					Draft                   bool
				}
				if json.NewDecoder(r.Body).Decode(&payload) != nil || !payload.Draft || payload.Head != "bugsnitch/"+id || payload.Base != "main" || !strings.Contains(payload.Body, "Fix threshold handling") {
					t.Fatal("bad draft payload")
				}
				writes.Add(1)
				saved = payload.Body
				return nil, errors.New("fixture lost response")
			}
			obj = []any{}
			if saved != "" && r.URL.Query().Get("head") == "example:bugsnitch/"+id {
				obj = []any{map[string]any{"number": 18, "body": saved, "state": "open", "draft": true, "user": map[string]string{"type": "Bot", "login": "bugs-fixture[bot]"}, "head": map[string]any{"ref": "bugsnitch/" + id, "sha": head, "repo": map[string]int{"id": 11}}, "base": map[string]any{"ref": "main", "sha": base, "repo": map[string]int{"id": 11}}}}
			}
		default:
			t.Fatalf("unexpected provider operation %s", r.URL)
		}
		raw, _ := json.Marshal(obj)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	app, err := github.NewLoopbackFixture("fixture", file, origin)
	if err != nil {
		t.Fatal(err)
	}
	s := Store{DB: db, App: app}
	token := strings.Repeat("c", 64)
	exec("UPDATE devices SET token_hash=$1 WHERE id='d'", token)
	delivery := func(op, key, attempt, target string, report, approval int) (string, error) {
		var out struct {
			Status string `json:"status"`
		}
		err := storage.Coordinate(ctx, db, "device.draft_delivery", map[string]any{"tokenHash": key, "request": map[string]any{"protocol": 1, "operation": op, "attemptId": attempt, "version": report, "approvalVersion": approval, "candidateSha": head, "baseSha": base, "target": target}}, &out)
		return out.Status, err
	}
	if status, err := delivery("status", token, id, "main", 5, 0); err != nil || status != "awaiting_approval" {
		t.Fatal("missing owner approval", status, err)
	}
	if _, err := delivery("published", token, id, "main", 5, 1); err == nil {
		t.Fatal("device created a draft intent")
	}
	request.Delivery, request.Candidate = "local_ship", head
	for range 2 {
		d, err := s.RequestDraft(ctx, "personal", "p", id, request)
		if err != nil || d.Status != "awaiting_publication" {
			t.Fatal("expected one held intent", d, err)
		}
	}
	if err = s.syncDraft(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("worker selected an unpublished branch", err)
	}
	exec("INSERT INTO devices(id,workspace_id,name,platform,token_hash) VALUES('other','personal','other','test',$1)", strings.Repeat("e", 64))
	for _, tc := range []struct{ key, target string }{{strings.Repeat("e", 64), "main"}, {token, "production"}} {
		if _, err := delivery("published", tc.key, id, tc.target, 5, 1); err == nil {
			t.Fatal("wrong device or target accepted")
		}
	}
	exec("UPDATE devices SET revoked=true WHERE id='d'")
	if _, err := delivery("published", token, id, "main", 5, 1); err == nil {
		t.Fatal("revoked device released draft")
	}
	exec("UPDATE devices SET revoked=false WHERE id='d'")
	for range 2 {
		status, err := delivery("published", token, id, "main", 5, 1)
		if err != nil || status != "queued" {
			t.Fatal("lost release reconciliation", status, err)
		}
	}
	var releaseEvents int
	if err = db.QueryRow("SELECT count(*) FROM events WHERE kind='draft.publication_confirmed'").Scan(&releaseEvents); err != nil || releaseEvents != 1 {
		t.Fatal("duplicate confirmation", releaseEvents, err)
	}
	if err = s.syncDraft(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.Drafts(ctx, "personal")
	if err != nil || len(list) != 1 || list[0].Status != "reconciling" || writes.Load() != 1 {
		t.Fatal(list, writes.Load(), err)
	}
	exec("UPDATE draft_pr_intents SET next_run=now()")
	if err = s.syncDraft(ctx); err != nil {
		t.Fatal(err)
	}
	list, err = s.Drafts(ctx, "personal")
	if err != nil || list[0].Number == nil || *list[0].Number != 18 || writes.Load() != 1 {
		t.Fatal(list, writes.Load(), err)
	}
	if status, err := delivery("status", token, id, "main", 5, 0); err != nil || status != "created" {
		t.Fatal("missing device receipt", status, err)
	}
	withdraw := strings.Repeat("3", 32)
	exec("INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version,evidence,issue_updated_at) SELECT $1,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version,evidence,issue_updated_at FROM attempt_reports WHERE id=$2", withdraw, id)
	if _, err = s.RequestDraft(ctx, "personal", "p", withdraw, request); err != nil {
		t.Fatal(err)
	}
	cancel := request
	cancel.Delivery, cancel.ApprovalVersion = "withdraw", 1
	if d, err := s.RequestDraft(ctx, "personal", "p", withdraw, cancel); err != nil || d.Status != "withdrawn" || d.ApprovalVersion != 2 {
		t.Fatal("withdraw", d, err)
	}
	if _, err := delivery("published", token, withdraw, "main", 5, 1); err == nil {
		t.Fatal("stale release accepted after withdrawal")
	}
	if _, err = s.RequestDraft(ctx, "personal", "p", withdraw, request); !errors.Is(err, ErrConflict) {
		t.Fatal("replayed original approval", err)
	}
	renew := request
	renew.ApprovalVersion = 2
	for range 2 {
		d, err := s.RequestDraft(ctx, "personal", "p", withdraw, renew)
		if err != nil || d.Status != "awaiting_publication" || d.ApprovalVersion != 3 {
			t.Fatal("renew same subject", d, err)
		}
	}
	if _, err = s.RequestDraft(ctx, "personal", "p", withdraw, cancel); !errors.Is(err, ErrConflict) {
		t.Fatal("stale withdrawal cancelled new approval", err)
	}
	if _, err := delivery("published", token, withdraw, "main", 5, 1); err == nil {
		t.Fatal("old release accepted for new approval")
	}
	// A sync advances the local version. The owner must still be able to
	// withdraw the earlier receipt and approve the current target/version.
	exec("UPDATE attempt_reports SET version=6 WHERE id=$1", withdraw)
	cancel.ApprovalVersion = 3
	if d, err := s.RequestDraft(ctx, "personal", "p", withdraw, cancel); err != nil || d.ApprovalVersion != 4 {
		t.Fatal("withdraw after new report", d, err)
	}
	if _, err = s.RequestDraft(ctx, "personal", "p", withdraw, renew); !errors.Is(err, ErrConflict) {
		t.Fatal("old reapproval resurrected withdrawn job", err)
	}
	renew.Version, renew.Target, renew.ApprovalVersion = 6, "staging", 4
	if d, err := s.RequestDraft(ctx, "personal", "p", withdraw, renew); err != nil || d.ReportVersion != 6 || d.Target != "staging" || d.ApprovalVersion != 5 {
		t.Fatal("renew current report", d, err)
	}
	if status, err := delivery("status", token, withdraw, "staging", 6, 5); err != nil || status != "awaiting_publication" {
		t.Fatal("current release unavailable", status, err)
	}
	cancel.Version, cancel.Target, cancel.ApprovalVersion = 6, "staging", 5
	if _, err = s.RequestDraft(ctx, "personal", "p", withdraw, cancel); err != nil {
		t.Fatal(err)
	}
	// A completed delivery and its PR number must never become a new draft.
	if _, err = s.RequestDraft(ctx, "personal", "p", withdraw, renew); !errors.Is(err, ErrConflict) {
		t.Fatal("same-subject stale reapproval resurrected job", err)
	}
	created := request
	created.ApprovalVersion = 1
	if _, err = s.RequestDraft(ctx, "personal", "p", id, created); !errors.Is(err, ErrConflict) {
		t.Fatal("reopened created draft", err)
	}
	if status, err := delivery("status", token, id, "main", 5, 1); err != nil || status != "created" {
		t.Fatal("lost prior draft", status, err)
	}
	request.Delivery, request.Candidate = "", ""
	if _, err = s.RequestDraft(ctx, "personal", "p", second, request); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE projects SET allow_draft_pr=false,policy_version=2")
	if err = s.syncDraft(ctx); err != nil {
		t.Fatal(err)
	}
	list, err = s.Drafts(ctx, "personal")
	if err != nil || len(list) != 3 || list[0].Status != "blocked" || writes.Load() != 1 {
		t.Fatal("stale policy produced a side effect", list, writes.Load(), err)
	}
}
