// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"github.com/lucasmkrx/bugsnitch/internal/testserver"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestChatDecisionAcrossSignedEndpoints(t *testing.T) {
	s := fixtureWithIntegrations(t, testserver.Config{SlackSecret: "test-slack", TelegramSecret: "test-telegram"})
	owner := login(t, s)
	p := owner.post(t, "projects", object{"name": "API", "repository": "example/api"}, 201)["id"].(string)
	m := owner.post(t, "members", object{"name": "Approver", "role": "approver", "projectId": p}, 201)["id"].(string)
	bind := "members/" + m + "/chat"
	owner.post(t, bind, object{"provider": "slack", "tenant": "TABC", "user": "UABC"}, 200)
	owner.post(t, bind, object{"provider": "telegram", "tenant": "private", "user": "100"}, 200)
	owner.post(t, "projects/"+p+"/dispatch", object{"paused": true, "version": 1}, 200)
	id := owner.post(t, "projects/"+p+"/dispatch", object{"paused": false, "version": 2}, 202)["decisionId"].(string)
	slack := func(team, cmd, secret string, ts int64, want int) string {
		t.Helper()
		v := url.Values{"command": {"/bugsnitch"}, "team_id": {team}, "user_id": {"UABC"}, "text": {cmd}}
		raw := v.Encode()
		h := hmac.New(sha256.New, []byte(secret))
		fmt.Fprintf(h, "v0:%d:%s", ts, raw)
		r := httptest.NewRequest("POST", "/hooks/slack", strings.NewReader(raw))
		r.Header.Set("X-Slack-Signature", fmt.Sprintf("v0=%x", h.Sum(nil)))
		r.Header.Set("X-Slack-Request-Timestamp", fmt.Sprint(ts))
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("Slack %d want %d: %s", w.Code, want, w.Body)
		}
		return w.Body.String()
	}
	now := time.Now().Unix()
	slack("TABC", "list", "wrong", now, 403)
	slack("TABC", "list", "test-slack", now-600, 403)
	slack("TOTHER", "list", "test-slack", now, 403)
	if !strings.Contains(slack("TABC", "list", "test-slack", now, 200), id) {
		t.Fatal("no scoped ask")
	}
	tele := func(chat int, want int) string {
		raw := fmt.Sprintf(`{"message":{"chat":{"id":%d,"type":"private"},"date":%d,"from":{"id":100,"is_bot":false},"text":"/bugsnitch resume %s 1"}}`, chat, now, id)
		r := httptest.NewRequest("POST", "/hooks/telegram", strings.NewReader(raw))
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test-telegram")
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	tele(101, 403)
	for range 2 {
		if !strings.Contains(tele(100, 200), "Resume dispatch recorded") {
			t.Fatal("did not confirm decision")
		}
	}
	if strings.Contains(slack("TABC", "list", "test-slack", now, 200), id) {
		t.Fatal("answered decision still pending")
	}
	owner.post(t, "members/"+m+"/revoke", object{}, 200)
	slack("TABC", "list", "test-slack", now, 403)
	var n int
	if e := s.DB.QueryRow("SELECT count(*) FROM events WHERE kind='decision.answered' AND actor_source='approver' AND actor_id=$1", m).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
