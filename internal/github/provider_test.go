// SPDX-License-Identifier: MPL-2.0
package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureKey(t *testing.T, key *rsa.PrivateKey, pkcs8 bool) string {
	t.Helper()
	b := pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	if pkcs8 {
		var err error
		b.Type = "PRIVATE KEY"
		b.Bytes, err = x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(t.TempDir(), "fixture.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&b), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func verifyJWT(t *testing.T, key *rsa.PublicKey, bearer string) {
	t.Helper()
	token, ok := strings.CutPrefix(bearer, "Bearer ")
	parts := strings.Split(token, ".")
	if !ok || len(parts) != 3 {
		t.Error("invalid JWT envelope")
		return
	}
	decode := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Error("invalid JWT encoding")
		}
		return b
	}
	var header struct{ Alg, Typ string }
	var claims struct {
		Iat, Exp int64
		Iss      string
	}
	if json.Unmarshal(decode(parts[0]), &header) != nil || json.Unmarshal(decode(parts[1]), &claims) != nil {
		t.Error("invalid JWT claims")
		return
	}
	now := time.Now().Unix()
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if header.Alg != "RS256" || header.Typ != "JWT" || claims.Iss != "fixture" || claims.Iat < now-70 || claims.Iat > now-55 || claims.Exp < now+465 || claims.Exp > now+485 || rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], decode(parts[2])) != nil {
		t.Error("JWT signature, scope or lifetime mismatch")
	}
}

func TestProviderJWTScopedAuthRateLimitAndRedirect(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const token = "fixture-installation-token"
	const secret = "PRIVATE_FIXTURE_MARKER"
	var redirects, mode atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-GitHub-Api-Version") != APIVersion || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("wrong GitHub version or media type")
		}
		switch r.URL.Path {
		case "/app":
			verifyJWT(t, &key.PublicKey, r.Header.Get("Authorization"))
			fmt.Fprint(w, `{"id":77,"slug":"bugs"}`)
		case "/app/installations/2/access_tokens":
			verifyJWT(t, &key.PublicKey, r.Header.Get("Authorization"))
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"permissions":{"issues":"read"},"repository_ids":[3]}` && string(b) != `{"repository_ids":[3],"permissions":{"issues":"read"}}` {
				t.Error("token permissions broadened")
			}
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"token":%q}`, token)
		case "/repositories/3":
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("wrong token scope")
			}
			fmt.Fprint(w, `{"id":3,"full_name":"org/project"}`)
		case "/repos/org/project/issues/9":
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("wrong issue authorization")
			}
			switch mode.Load() {
			case 0:
				w.Header().Set("Retry-After", "83")
				w.WriteHeader(429)
				fmt.Fprint(w, secret)
			case 1:
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			case 2:
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Unix()+100))
				w.WriteHeader(403)
				fmt.Fprint(w, secret)
			default:
				fmt.Fprint(w, `{"id":20,"number":9,"pull_request":{},"state":"open"}`)
			}
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	app, err := NewLoopbackFixture("fixture", fixtureKey(t, key, true), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	read := func() error { _, err := app.ReadIssue(context.Background(), 2, 3, "org/project", 9); return err }
	var api *APIError
	if err = read(); !errors.As(err, &api) || api.Status != 429 || api.RetryAfter != 83*time.Second || strings.Contains(err.Error(), secret) {
		t.Fatal("invalid rate-limit classification", err)
	}
	mode.Store(1)
	if err = read(); !errors.As(err, &api) || api.Status != 307 || redirects.Load() != 0 {
		t.Fatal("redirect escaped the configured origin", err)
	}
	mode.Store(2)
	if err = read(); !errors.As(err, &api) || api.Status != 403 || api.RetryAfter < 99*time.Second || api.RetryAfter > 101*time.Second {
		t.Fatal("rate-limit reset not preserved", err)
	}
	mode.Store(3)
	if err = read(); !errors.Is(err, ErrSubjectChanged) {
		t.Fatal("pull request accepted as canonical issue", err)
	}
}

func TestDiscoverRepositoryInConfiguredInstallation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var wrong atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app":
			fmt.Fprint(w, `{"id":8,"slug":"fixture"}`)
		case "/app/installations/2/access_tokens":
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"permissions":{"metadata":"read"},"repositories":["project"]}` {
				t.Errorf("token scope: %s", body)
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{"token":"local-fixture"}`)
		case "/repos/org/project":
			if wrong.Load() {
				fmt.Fprint(w, `{"id":31,"full_name":"other/project"}`)
			} else {
				fmt.Fprint(w, `{"id":31,"full_name":"ORG/project"}`)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()
	app, err := NewLoopbackFixture("fixture", fixtureKey(t, key, true), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := app.RepositoryID(t.Context(), 2, "org/project"); id != 31 || err != nil {
		t.Fatal(id, err)
	}
	wrong.Store(true)
	if _, err := app.RepositoryID(t.Context(), 2, "org/project"); err == nil {
		t.Fatal("accepted wrong repository")
	}
}
