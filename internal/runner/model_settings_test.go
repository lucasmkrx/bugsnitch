// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelSettingsStayInLocalProfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUGSNITCH_HOME", dir)
	host, token := "127.0.0.1:12345", strings.Repeat("a", 64)
	handler := modelSettingsHandler(host, "https://dashboard.example", token)
	request := func(origin, bearer, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://"+host+"/settings", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+bearer)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	const key = "sk-synthetic-fixture-key"
	const save = `{"action":"save","apiKey":"` + key + `","model":"gpt-fixture"}`
	for _, r := range []*httptest.ResponseRecorder{
		request("https://dashboard.example", token, save),
		request("http://"+host, "wrong", save),
	} {
		if r.Code != http.StatusForbidden {
			t.Fatal("unauthorized local settings write accepted")
		}
	}
	if configured, err := UsesOpenAIAPI(); err != nil || configured {
		t.Fatal("unconfigured profile unexpectedly uses the API", err)
	}
	action := func(body string) modelSettingsReply {
		r := request("http://"+host, token, body)
		if r.Code != http.StatusOK || strings.Contains(r.Body.String(), key) {
			t.Fatal("invalid or revealing settings response")
		}
		var reply modelSettingsReply
		if err := json.Unmarshal(r.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	if reply := action(save); reply.Error != "" || reply.Model != "gpt-fixture" {
		t.Fatal(reply)
	}
	info, err := os.Stat(filepath.Join(dir, modelSettingsFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("the credential file must be private", err)
	}
	if reply := action(`{"action":"save","model":"gpt-new"}`); reply.Error != "" || reply.Model != "gpt-new" {
		t.Fatal(reply)
	}
	settings, err := loadOpenAISettings()
	if err != nil || settings.Key != key || settings.Model != "gpt-new" {
		t.Fatal("model change did not preserve the saved key", err)
	}
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	if reply := action(`{"action":"status"}`); reply.Model != "" || reply.Error != "" {
		t.Fatal("another local profile can see the saved settings")
	}
	t.Setenv("BUGSNITCH_HOME", dir)
	if reply := action(`{"action":"remove"}`); reply.Error != "" {
		t.Fatal(reply)
	}
	if configured, err := UsesOpenAIAPI(); err != nil || configured {
		t.Fatal("removed key did not restore the default runtime", err)
	}
}

func TestPairedModelSettingsRequiresWorkspacePermit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUGSNITCH_HOME", dir)
	const host = "127.0.0.1:43182"
	expected := ModelSettingsGrant{DeviceID: "fixture", Expiry: 200, Proof: "owner-permit"}
	var issuedDigest string
	handler := modelSettingsRoutes(host, "https://dashboard.example", "", func(_ context.Context, digest string, permit *ModelSettingsGrant) (ModelSettingsGrant, error) {
		if permit == nil {
			issuedDigest = digest
			return ModelSettingsGrant{DeviceID: "fixture", Expiry: 200, Proof: "runner-claim"}, nil
		}
		if digest != issuedDigest || *permit != expected {
			return ModelSettingsGrant{}, errors.New("invalid permit")
		}
		return ModelSettingsGrant{}, nil
	})
	request := func(path, origin, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	if r := request("/connect", "https://dashboard.example", "", "{}"); r.Code != http.StatusForbidden {
		t.Fatal("cross-origin connection request accepted")
	}
	r := request("/connect", "http://"+host, "", "{}")
	var session struct {
		Token string
		Claim ModelSettingsGrant
	}
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &session) != nil ||
		len(session.Token) != 64 || session.Claim.Proof != "runner-claim" {
		t.Fatal("paired runner did not issue a connection")
	}
	const key = "sk-synthetic-fixture-key"
	save := func(token string, permit *ModelSettingsGrant) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]any{
			"action": "save", "apiKey": key, "model": "gpt-fixture", "permit": permit,
		})
		if err != nil {
			t.Fatal(err)
		}
		return request("/settings", "http://"+host, token, string(body))
	}
	for _, rejected := range []*httptest.ResponseRecorder{
		save(session.Token, nil),
		save(session.Token, &session.Claim),
		save(strings.Repeat("a", 64), &expected),
	} {
		if rejected.Code != http.StatusForbidden {
			t.Fatal("a settings change without the matching workspace owner permit was accepted")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, modelSettingsFile)); !os.IsNotExist(err) {
		t.Fatal("a rejected request changed the local key")
	}
	r = save(session.Token, &expected)
	settings, err := loadOpenAISettings()
	if r.Code != http.StatusOK || strings.Contains(r.Body.String(), key) ||
		err != nil || settings.Key != key || settings.Model != "gpt-fixture" {
		t.Fatal("the authorized settings change did not remain local")
	}
}
