// SPDX-License-Identifier: MPL-2.0
package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateConnectionRotationAndFailedUpdate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	origin := "https://one.example"
	old := strings.Repeat("a", 32)
	next := strings.Repeat("b", 32)
	if err := saveProfile(dir, origin, strings.NewReader(old+"\n")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, profileName): 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("private mode required on %s", filepath.Base(path))
		}
	}
	for _, invalid := range []struct{ origin, secret string }{
		{"http://untrusted.example", next}, {"https://owner:embedded@wrong.example", next}, {origin, old + "\n" + next}, {origin, strings.Repeat("a", tokenLimit+1)},
	} {
		err := saveProfile(dir, invalid.origin, strings.NewReader(invalid.secret))
		if err == nil || strings.Contains(err.Error(), next) || strings.Contains(err.Error(), old) {
			t.Fatal("invalid update should fail without including input")
		}
		got, err := readProfile(dir)
		if err != nil || got.Origin != origin || got.Token != old {
			t.Fatal("failed update changed the previous connection")
		}
	}
	if err := saveProfile(dir, "https://two.example", strings.NewReader(next+"\r\n")); err != nil {
		t.Fatal(err)
	}
	got, err := readProfile(dir)
	if err != nil || got.Origin != "https://two.example" || got.Token != next {
		t.Fatal("update must replace both origin and token")
	}
}

func TestSettingsSourcesStayBound(t *testing.T) {
	token := strings.Repeat("f", 32)
	// A supplied origin never borrows even an available saved credential.
	if _, err := loadConnection("https://different.example", ""); err == nil {
		t.Fatal("origin override without its own token was accepted")
	}
	got, err := loadConnection("https://new.example", token)
	if err != nil || got.Origin != "https://new.example" || got.Token != token {
		t.Fatal("environment pair should work")
	}
	got, err = loadConnection("", token)
	if err != nil || got.Origin != defaultOrigin {
		t.Fatal("standalone environment token should use legacy loopback origin")
	}
	b := bearer{origin: "https://one.example", token: token}
	for _, address := range []string{"https://other.example", "http://one.example", "https://one.example:444", "https://person@one.example"} {
		req, err := http.NewRequest(http.MethodGet, address+"/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = b.RoundTrip(req); err == nil || req.Header.Get("Authorization") != "" {
			t.Fatal("unexpected authorization for another origin")
		}
	}
}

func TestProfileRejectsSharedFilesAndLinks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bugsnitch-mcp")
	if err := saveProfile(dir, defaultOrigin, strings.NewReader(strings.Repeat("s", 32))); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, profileName)
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readProfile(dir); err == nil {
		t.Fatal("group-readable credential was accepted")
	}
	if err := os.Rename(file, file+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file+".original", file); err != nil {
		t.Fatal(err)
	}
	if _, err := readProfile(dir); err == nil {
		t.Fatal("linked credential was accepted")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := saveProfile(dir, defaultOrigin, strings.NewReader("test-token")); err == nil {
		t.Fatal("saved credential into shared directory")
	}
}
