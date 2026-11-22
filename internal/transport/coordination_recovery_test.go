// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

// The proxy forwards real frames to the shipped Rust process and discards its
// first successful revocation reply. This puts the loss strictly after COMMIT.
func TestLostNativeReplyReconcilesCommittedRevocation(t *testing.T) {
	if os.Getenv("BUGSNITCH_TEST_DSN") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	helper := os.Getenv("BUGSNITCH_MIGRATOR")
	if helper == "" {
		t.Fatal("set BUGSNITCH_MIGRATOR to the matching Rust binary")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	proxy, seen := filepath.Join(dir, "proxy"), filepath.Join(dir, "calls")
	source := fmt.Sprintf(`#!%s
import json, os, struct, subprocess, sys
helper, seen = %q, %q
if sys.argv[1:] != ["--coordinate"]:
    os.execv(helper, [helper, *sys.argv[1:]])
while header := sys.stdin.buffer.read(4):
    payload = sys.stdin.buffer.read(struct.unpack(">I", header)[0])
    reply = subprocess.run([helper, "--coordinate"], input=header+payload, stdout=subprocess.PIPE, check=True).stdout
    if json.loads(payload)["op"] == "device.revoke" and "value" in json.loads(reply[4:]):
        with open(seen, "a") as f:
            f.write("committed\n")
            if f.tell() == len("committed\n"):
                sys.exit(0)
    sys.stdout.buffer.write(reply)
    sys.stdout.buffer.flush()
`, python, helper, seen)
	if err = os.WriteFile(proxy, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUGSNITCH_MIGRATOR", proxy)
	s := fixture(t)

	if _, err = s.DB.Exec("INSERT INTO devices(id,workspace_id,name,platform) VALUES ('lost-reply','personal','test','linux')"); err != nil {
		t.Fatal(err)
	}
	revoke := func() error {
		return storage.Coordinate(context.Background(), s.DB, "device.revoke", object{"workspace": "personal", "device": "lost-reply"}, nil)
	}
	if err = revoke(); !errors.Is(err, storage.ErrUnknownOutcome) {
		t.Fatal("lost commit reply should have an unknown outcome", err)
	}
	var committed bool
	if err = s.DB.QueryRow("SELECT revoked FROM devices WHERE id='lost-reply'").Scan(&committed); err != nil || !committed {
		t.Fatalf("revocation committed=%v, err=%v", committed, err)
	}
	calls, err := os.ReadFile(seen)
	if err != nil || string(calls) != "committed\n" {
		t.Fatal("bridge replayed lost response", err)
	}
	if err = revoke(); err != nil {
		t.Fatal("explicit retry failed", err)
	}
	var n int
	if err = s.DB.QueryRow("SELECT count(*) FROM events WHERE kind='device.revoked' AND subject_id='lost-reply'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("reconciled events=%d, err=%v", n, err)
	}
	calls, err = os.ReadFile(seen)
	if err != nil || strings.Count(string(calls), "committed\n") != 2 {
		t.Fatal("expected exactly one explicit retry", err)
	}
}

// Drop the response only after the packaged server completed the request.
// This exercises owner reconciliation at the public HTTP boundary even when
// reverse proxies or the browser disconnect after a successful write.
func TestLostHTTPResponseAfterNativeRevocation(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	if _, err := s.DB.Exec("INSERT INTO devices(id,workspace_id,name,platform) VALUES('ack-lost','personal','first','darwin/arm64')"); err != nil {
		t.Fatal(err)
	}
	var completed atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		owner.handler.ServeHTTP(rec, r)
		if rec.Code != 200 {
			t.Errorf("native server status %d", rec.Code)
			w.WriteHeader(rec.Code)
			return
		}
		completed.Add(1)
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("drop reply: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer proxy.Close()
	path := "/api/v1/devices/ack-lost/revoke"
	req, err := http.NewRequest("POST", proxy.URL+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(owner.cookie)
	response, err := proxy.Client().Do(req)
	if err == nil {
		response.Body.Close()
		t.Fatal("client unexpectedly received an acknowledgment")
	}
	if completed.Load() != 1 {
		t.Fatalf("completed native writes: %d", completed.Load())
	}
	code, state := owner.req(t, "GET", "/api/v1/state", nil, "")
	if code != 200 {
		t.Fatal(code)
	}
	devices := state["devices"].([]any)
	if len(devices) != 1 || devices[0].(map[string]any)["revoked"] != true {
		t.Fatal("fresh state did not show committed revocation")
	}
	owner.post(t, "devices/ack-lost/revoke", object{}, 200)
	var events int
	if err := s.DB.QueryRow("SELECT count(*) FROM events WHERE kind='device.revoked' AND subject_id='ack-lost'").Scan(&events); err != nil || events != 1 {
		t.Fatalf("observed %d revocations: %v", events, err)
	}
}
