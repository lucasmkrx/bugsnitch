// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardServesRegisteredAssetsAndNavigation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	publish := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const page = "<!doctype html><title>Bugsnitch</title>"
	const css = "body { color: navy; }"
	publish("index.html", page)
	publish("assets/theme.css", css)
	handler := fixtureWithWeb(t, dir).handler
	request := func(method, url string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, url, nil))
		return w
	}
	for _, route := range []string{"/", "/plans/selected"} {
		got := request("GET", route)
		if got.Code != http.StatusOK || got.Body.String() != page || !strings.HasPrefix(got.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %q", route, got.Code, got.Body.String())
		}
	}
	got := request("GET", "/assets/theme.css")
	if got.Code != http.StatusOK || got.Body.String() != css || !strings.HasPrefix(got.Header().Get("Content-Type"), "text/css") {
		t.Fatal(got.Code, got.Body.String(), got.Header())
	}
	got = request("HEAD", "/assets/theme.css")
	if got.Code != http.StatusOK || got.Body.Len() != 0 || got.Header().Get("Content-Length") == "" {
		t.Fatal(got.Code, got.Header())
	}
	got = request("GET", "/index.html?view=plans")
	if got.Code != http.StatusMovedPermanently || got.Header().Get("Location") != "./?view=plans" {
		t.Fatal(got.Code, got.Header())
	}
	got = request("POST", "/plans/selected")
	if got.Code != http.StatusMethodNotAllowed || got.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal(got.Code, got.Header())
	}

	// A release deployed after startup becomes visible only in a fresh handler.
	publish("assets/theme.css", "body { color: green; }")
	publish("assets/new.css", "body { color: black; }")
	if got = request("GET", "/assets/theme.css"); got.Body.String() != css {
		t.Fatal("registered response changed")
	}
	if got = request("GET", "/assets/new.css"); got.Code != http.StatusNotFound {
		t.Fatalf("unregistered asset returned %d", got.Code)
	}
	updated := fixtureWithWeb(t, dir).handler
	w := httptest.NewRecorder()
	updated.ServeHTTP(w, httptest.NewRequest("GET", "/assets/new.css", nil))
	if w.Code != http.StatusOK || w.Body.String() != "body { color: black; }" {
		t.Fatal("new bundle missing after restart")
	}
}
