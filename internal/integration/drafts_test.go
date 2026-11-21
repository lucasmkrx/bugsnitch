package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
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
	app, err := github.NewApp("fixture", file)
	if err != nil {
		t.Fatal(err)
	}
	s := Store{DB: db, App: app}
	request := DraftRequest{Version: 5, PolicyVersion: 1, Target: "main", Summary: "Fix threshold handling", Behavior: "Qualifying orders have free shipping"}
	var saved string
	writes := 0
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = roundTrip(func(r *http.Request) (*http.Response, error) {
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
				writes++
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
	for i := 0; i < 2; i++ {
		d, err := s.RequestDraft(ctx, "personal", "p", id, request)
		if err != nil || d.Status != "queued" {
			t.Fatal("expected one queued intent", d, err)
		}
	}
	if err = s.syncDraft(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.Drafts(ctx, "personal")
	if err != nil || len(list) != 1 || list[0].Status != "reconciling" || writes != 1 {
		t.Fatal(list, writes, err)
	}
	exec("UPDATE draft_pr_intents SET next_run=now()")
	if err = s.syncDraft(ctx); err != nil {
		t.Fatal(err)
	}
	list, err = s.Drafts(ctx, "personal")
	if err != nil || list[0].Number == nil || *list[0].Number != 18 || writes != 1 {
		t.Fatal(list, writes, err)
	}
	if _, err = s.RequestDraft(ctx, "personal", "p", second, request); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE projects SET allow_draft_pr=false,policy_version=2")
	if err = s.syncDraft(ctx); err != nil {
		t.Fatal(err)
	}
	list, err = s.Drafts(ctx, "personal")
	if err != nil || len(list) != 2 || list[0].Status != "blocked" || writes != 1 {
		t.Fatal("stale policy produced a side effect", list, writes, err)
	}
}
