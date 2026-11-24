// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerConnectsOnlyInstallationRepository(t *testing.T) {
	target := fixture(t)
	owner := login(t, target)
	p := owner.post(t, "projects", object{"name": "Project", "repository": "example/web"}, 201)["id"].(string)
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.pem")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app":
			fmt.Fprint(w, `{"id":7,"slug":"fixture"}`)
		case "/app/installations/22/access_tokens":
			w.WriteHeader(201)
			fmt.Fprint(w, `{"token":"test"}`)
		case "/repos/example/web":
			fmt.Fprint(w, `{"id":31,"full_name":"example/web"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	app, err := github.NewLoopbackFixture("fixture", path, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	service := Server{DB: target.DB, GitHubInstallation: 22, GitHubApp: app}
	send := func(ver int, want int) {
		in, _ := json.Marshal(object{"workspace": "personal", "project": p, "body": object{"version": ver}})
		r := service.Domain(t.Context(), DomainRequest{Version: 1, Operation: "project.connect_github", Actor: [2]string{"owner", "owner"}, Input: in})
		if r.Status != want {
			t.Fatalf("connect %d: %+v", ver, r)
		}
	}
	send(1, 200)
	send(1, 409)
	send(2, 200)
	var install, repo int
	var active bool
	if err = target.DB.QueryRow("SELECT installation_id,repository_id,active FROM github_mappings WHERE project_id=$1", p).Scan(&install, &repo, &active); err != nil || install != 22 || repo != 31 || !active {
		t.Fatal(install, repo, active, err)
	}
	service.GitHubInstallation = 99
	send(2, 409)
}
