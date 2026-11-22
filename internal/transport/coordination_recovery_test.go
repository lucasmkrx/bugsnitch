// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	c := login(t, s)
	if _, err = s.DB.Exec("INSERT INTO devices(id,workspace_id,name,platform) VALUES ('lost-reply','personal','test','linux')"); err != nil {
		t.Fatal(err)
	}
	status, body := c.req(t, "POST", "/api/v1/devices/lost-reply/revoke", object{}, "")
	var committed bool
	if err = s.DB.QueryRow("SELECT revoked FROM devices WHERE id='lost-reply'").Scan(&committed); err != nil || !committed || status != 503 || !strings.Contains(body["error"].(string), "outcome unknown") {
		t.Fatalf("revocation committed=%v, HTTP=%d, err=%v", committed, status, err)
	}
	calls, err := os.ReadFile(seen)
	if err != nil || string(calls) != "committed\n" {
		t.Fatal("bridge replayed lost response", err)
	}
	c.post(t, "devices/lost-reply/revoke", object{}, 200)
	var n int
	if err = s.DB.QueryRow("SELECT count(*) FROM events WHERE kind='device.revoked' AND subject_id='lost-reply'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("reconciled events=%d, err=%v", n, err)
	}
	calls, err = os.ReadFile(seen)
	if err != nil || strings.Count(string(calls), "committed\n") != 2 {
		t.Fatal("expected exactly one explicit retry", err)
	}
}
