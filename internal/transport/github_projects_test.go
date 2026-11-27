// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

func TestGitHubProjectIntakeSharesCanonicalLedger(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	p := owner.post(t, "projects", object{"name": "Project", "repository": "example/web"}, 201)["id"].(string)
	path := "projects/" + p + "/github-project/"
	owner.post(t, path+"issues", object{"number": 0}, 400)
	owner.post(t, path+"import", object{"issue": 7, "number": 1, "itemId": "invalid id"}, 400)
	owner.post(t, path+"issues", object{"number": 1}, 503)
	if code, _ := (system{handler: s.handler}).req(t, "POST", "/api/v1/"+path+"import", object{}, ""); code != 401 {
		t.Fatal(code)
	}
	if _, err := s.DB.Exec("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal',$1,22,31)", p); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	i := github.Issue{ID: 47, Number: 7, Title: "Fix sidebar", Body: "initial", UpdatedAt: "2026-11-26T10:00:00Z", State: "open"}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "key.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err = os.WriteFile(keyFile, pemBytes, 0600); err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[github.Issue]
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := current.Load()
		switch r.URL.Path {
		case "/app":
			fmt.Fprint(w, `{"id":1,"slug":"factory"}`)
		case "/app/installations/22/access_tokens":
			w.WriteHeader(201)
			fmt.Fprint(w, `{"token":"fixture"}`)
		case "/repositories/31":
			fmt.Fprint(w, `{"id":31,"node_id":"R_31","full_name":"example/web"}`)
		case "/repos/example/web/issues/7":
			json.NewEncoder(w).Encode(object{"id": issue.ID, "node_id": "I_47", "number": issue.Number, "title": issue.Title, "body": issue.Body, "state": issue.State, "updated_at": issue.UpdatedAt})
		case "/graphql":
			var query struct{ Variables struct{ Item string } }
			if json.NewDecoder(r.Body).Decode(&query) != nil {
				t.Error("Invalid GraphQL variables")
			}
			item := object{"id": query.Variables.Item, "isArchived": false,
				"content": object{"__typename": "Issue", "id": "I_47", "number": 7, "state": "OPEN", "title": issue.Title, "repository": object{"nameWithOwner": "example/web", "id": "R_31"}},
				"project": object{"id": "board", "number": 3, "closed": false, "owner": object{"__typename": "Organization", "login": "example"}}}
			json.NewEncoder(w).Encode(object{"data": object{"node": item}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	app, err := github.NewLoopbackFixture("test", keyFile, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	service := Server{DB: s.DB, GitHubApp: app}
	record := func(item string, status int) {
		t.Helper()
		snapshot := i
		current.Store(&snapshot)
		request, err := json.Marshal(object{"workspace": "personal", "project": p, "body": object{"number": 3, "issue": 7, "itemId": item}})
		if err != nil {
			t.Fatal(err)
		}
		response := service.Domain(ctx, DomainRequest{Version: 1, Operation: "github_project.import", Actor: [2]string{"owner", "owner"}, Input: request})
		if response.Status != status {
			t.Fatal(response)
		}
	}
	record("item", 200)
	record("item", 200)
	i.Body = "changed"
	i.UpdatedAt = "2026-11-26T10:05:00Z"
	record("wrong", 409)
	var generation int
	var revision, digest string
	read := func(want int) {
		t.Helper()
		err := s.DB.QueryRow("SELECT generation,selection_revision,digest FROM planned_issue_state WHERE project_id=$1 AND number=7", p).Scan(&generation, &revision, &digest)
		if err != nil || generation != want {
			t.Fatal(err, generation, want)
		}
	}
	read(1)
	record("item", 200)
	read(2)
	if revision != i.UpdatedAt || digest != i.RequirementsDigest() {
		t.Fatal("selection and runner digest diverged")
	}
	i.Title = "password: private"
	record("item", 409)
	i.Title = "Fix sidebar"
	i.ID = 99
	record("item", 409)
	read(2)
	var events int
	if err = s.DB.QueryRow("SELECT count(*) FROM events WHERE project_id=$1 AND kind='github_project.imported'", p).Scan(&events); err != nil || events != 1 {
		t.Fatal("import events", events, err)
	}
}
