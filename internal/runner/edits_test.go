// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
)

func TestFeatureFromDiscoveredDirectory(t *testing.T) {
	repo, issue := fixture(t)
	if err := os.Mkdir(filepath.Join(repo, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"src/old.sh": "echo move-me\n", "src/obsolete": "old", "src/.hidden": "private", "verify-feature.sh": "set -eu\n/bin/sh tests/checkout.sh\ntest ! -e src/obsolete\ntest ! -e src/old.sh\ntest -x src/moved.sh\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "seed existing sources"}} {
		if _, err := git(repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{EditDirectories: []string{"src", "tests"}, Validate: []string{"/bin/sh", "verify-feature.sh"}}
	if err := AddProject("personal", issue.Repository, repo, cfg); err != nil {
		t.Fatal(err)
	}
	modelTurn = func(_ context.Context, a *Attempt, _ string, prompt string, _ any) (agent.Result, error) {
		switch a.Phase {
		case "plan":
			if !strings.Contains(prompt, "move-me") || strings.Contains(prompt, ".hidden") {
				t.Fatalf("unexpected discovery: %s", prompt)
			}
			return agent.Result{Text: "Create the checkout module and test."}, nil
		case "implement":
			ops := []edit{
				{Operation: "create", Path: "src/checkout/fee.sh", Content: "fee() { echo 0; }\n"},
				{Operation: "create", Path: "tests/checkout.sh", Content: "set -eu\n. ./src/checkout/fee.sh\ntest \"$(fee)\" = 0\n"},
				{Operation: "rename", Path: "src/old.sh", Destination: "src/moved.sh"},
				{Operation: "delete", Path: "src/obsolete"},
			}
			b, err := json.Marshal(implementation{"Add checkout fee module and coverage", ops})
			return agent.Result{Text: string(b)}, err
		default:
			return agent.Result{Text: `{"summary":"The newly added checkout test passes.","findings":[]}`}, nil
		}
	}
	a, err := Start(context.Background(), "personal", issue.Repository, issue)
	if err != nil {
		t.Fatal(err)
	}
	a, err = ContinueToCandidate(context.Background(), a.ID, "", a.Version)
	if err != nil || a.State != "waiting" || a.Phase != "validate" || a.CandidateSHA == "" {
		t.Fatal(a.Summary, err)
	}
	pinned := a.CandidateSHA
	a, err = ContinueAt(context.Background(), a.ID, "", a.Version)
	if err != nil || a.CandidateSHA != pinned || a.State != "completed" {
		t.Fatal(a.Summary, err)
	}
	got, diff, err := DiffForSharing(a.ID)
	if err != nil || got.ID != a.ID || !strings.Contains(diff, "tests/checkout.sh") || !strings.Contains(diff, "src/obsolete") || strings.Contains(diff, ".hidden") {
		t.Fatal(diff, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "tests/checkout.sh")); !os.IsNotExist(err) {
		t.Fatal("modified original checkout", err)
	}
}

func TestDirectoryEditsRespectScope(t *testing.T) {
	repo, _ := fixture(t)
	if err := os.Mkdir(filepath.Join(repo, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, "src", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := Attempt{Worktree: repo, Project: Project{Config: Config{Files: []string{"example.txt"}, EditDirectories: []string{"src"}}}}
	for _, path := range []string{"outside.go", "src/../outside", "src/.git/config", "src/link/bad", "src/output.log", "src/../.env"} {
		if _, err := a.fileChanges(nil, []edit{{Operation: "create", Path: path}}); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if _, err := a.fileChanges(map[string]string{"example.txt": "broken"}, []edit{{Operation: "delete", Path: "example.txt"}}); err == nil {
		t.Fatal("deleted explicit file")
	}
}

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
