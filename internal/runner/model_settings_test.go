// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"encoding/json"
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
