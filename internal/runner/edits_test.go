// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsolatedValidator(t *testing.T) {
	if os.Getenv("BUGSNITCH_TEST_ISOLATED_VALIDATOR") != "1" {
		t.Skip("requires host sandbox permissions")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("requires supported platform sandbox")
	}
	a := validationAttempt(t)
	secret := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	a.Project.Config.ValidationSandbox = "isolated"
	a.Project.Config.Validate = []string{"/bin/sh", "-c", `set -eu
test "$(cat example.txt)" = broken
echo scratch > "$TMPDIR/value"
test "$(cat "$TMPDIR/value")" = scratch
if cat "$1" >/dev/null 2>&1; then exit 13; fi
if touch workspace-write >/dev/null 2>&1; then exit 14; fi`, "validate", secret}
	code, err := validate(context.Background(), &a)
	if err != nil || code == nil || *code != 0 {
		t.Fatalf("isolated validation %v, %v", code, err)
	}
}
