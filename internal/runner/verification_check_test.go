// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const checkFixtureOutput = `{"environment":"staging","revision":"build-123","observations":[{"criterion":"Shared note","result":"passed","evidence":"Co-teacher reopened the test note."},{"criterion":"Empty next week","result":"failed","evidence":"The note also appeared in the following week."}]}`

func TestVerificationCheckFixture(t *testing.T) {
	if len(os.Args) < 4 || os.Args[2] != "--" {
		return
	}
	switch os.Args[3] {
	case "observe":
		var request struct {
			Issue       Issue  `json:"issue"`
			Environment string `json:"environment"`
			Revision    string `json:"revision"`
		}
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil || request.Issue.Number != 42 || request.Issue.Criteria[0] != "Shared note" || request.Environment != "staging" || request.Revision != "build-123" {
			os.Exit(2)
		}
		if os.Getenv("BUGSNITCH_GITHUB_READ_TOKEN") != "" || os.Getenv("CLOUDSDK_CONFIG") != "" || os.Getenv("BUGSNITCH_HOME") != "" {
			os.Exit(3)
		}
		wd, err := os.Stat(".")
		home, homeErr := os.Stat(os.Getenv("HOME"))
		tmp, tmpErr := os.Stat(os.Getenv("TMPDIR"))
		if err != nil || homeErr != nil || tmpErr != nil || !os.SameFile(wd, home) || !os.SameFile(wd, tmp) || wd.Mode().Perm()&0077 != 0 {
			os.Exit(4)
		}
		credential, err := os.ReadFile(os.Args[4])
		if err != nil || string(credential) != "synthetic test account" || os.Args[5] != "$(touch injected)" {
			os.Exit(5)
		}
		if err := os.WriteFile("note.txt", []byte("temporary result"), 0600); err != nil {
			os.Exit(6)
		}
		fmt.Print(checkFixtureOutput)
	case "reply":
		fmt.Print(os.Args[4])
	case "overflow":
		fmt.Print(checkFixtureOutput + strings.Repeat(" ", 256<<10))
	case "nonzero":
		fmt.Fprint(os.Stderr, "unshared credential")
		fmt.Print(checkFixtureOutput)
		os.Exit(7)
	case "wait":
		time.Sleep(5 * time.Second)
	default:
		os.Exit(8)
	}
	os.Exit(0)
}

func verificationFixtureArgv(t *testing.T, args ...string) []string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{binary, "-test.run=^TestVerificationCheckFixture$", "--"}, args...)
}

func TestVerificationCheckUsesPrivateWorkingDirectoryAndExplicitArguments(t *testing.T) {
	checkout := t.TempDir()
	t.Chdir(checkout)
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "unshared credential")
	t.Setenv("CLOUDSDK_CONFIG", checkout)
	t.Setenv("BUGSNITCH_HOME", checkout)
	account := filepath.Join(t.TempDir(), "internal account.txt")
	if err := os.WriteFile(account, []byte("synthetic test account"), 0600); err != nil {
		t.Fatal(err)
	}
	argv := verificationFixtureArgv(t, "observe", account, "$(touch injected)")
	issue := Issue{Number: 42, Criteria: []string{"Shared note", "Empty next week"}}
	rows, err := runVerificationCheck(t.Context(), argv, issue, "staging", "build-123")
	if err != nil || len(rows) != 2 || rows[0].Result != "passed" || rows[1].Result != "failed" || rows[1].Criterion != issue.Criteria[1] {
		t.Fatalf("observations = %+v, error = %v", rows, err)
	}
	entries, err := os.ReadDir(checkout)
	if err != nil || len(entries) != 0 {
		t.Fatal("check wrote into the checkout", entries, err)
	}
}

func TestVerificationCheckRejectsInvalidReceipts(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"relative executable", []string{"not-absolute"}, "absolute executable"},
		{"exit failure", verificationFixtureArgv(t, "nonzero"), "status 7"},
		{"output limit", verificationFixtureArgv(t, "overflow"), "exceeds 256 KiB"},
		{"bad JSON", verificationFixtureArgv(t, "reply", "unshared credential"), "one JSON object"},
		{"second response", verificationFixtureArgv(t, "reply", checkFixtureOutput+" {}"), "one JSON object"},
		{"unknown field", verificationFixtureArgv(t, "reply", strings.Replace(checkFixtureOutput, `"result":`, `"unshared credential":true,"result":`, 1)), "one JSON object"},
		{"wrong environment", verificationFixtureArgv(t, "reply", strings.Replace(checkFixtureOutput, "staging", "wrong", 1)), "different environment or revision"},
		{"wrong revision", verificationFixtureArgv(t, "reply", strings.Replace(checkFixtureOutput, "build-123", "wrong", 1)), "different environment or revision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := runVerificationCheck(t.Context(), tc.args, Issue{}, "staging", "build-123")
			if rows != nil || err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "unshared credential") {
				t.Fatalf("rows = %v, error = %v", rows, err)
			}
		})
	}
	t.Run("caller timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		_, err := runVerificationCheck(ctx, verificationFixtureArgv(t, "wait"), Issue{}, "staging", "build-123")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout error = %v", err)
		}
	})
}

func TestVerificationCheckStopsShellDescendants(t *testing.T) {
	t.Run("no descendants", func(t *testing.T) {
		if err := runCheckGroup(t.Context(), exec.Command("/usr/bin/true")); err != nil {
			t.Fatal(err)
		}
	})
	for _, mode := range []string{"cancel", "launcher-exits"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			dir := t.TempDir()
			ready, leaked := filepath.Join(dir, "ready"), filepath.Join(dir, "leaked")
			// The child inherits the process group and stdout like a typical
			// shell-launched test tool. Record any work it does after return.
			const script = `
(echo ready > "$1"; sleep 1; echo leaked > "$2") &
while [ ! -f "$1" ]; do sleep 0.01; done
if [ "$3" = cancel ]; then wait; fi
printf '%s' "$4"
`
			done := make(chan error, 1)
			go func() {
				_, err := runVerificationCheck(ctx, []string{"/bin/sh", "-c", script, "fixture", ready, leaked, mode, checkFixtureOutput}, Issue{}, "staging", "build-123")
				done <- err
			}()
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("shell child never started")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if mode == "cancel" {
				cancel()
			}
			err := <-done
			if mode == "cancel" && !errors.Is(err, context.Canceled) || mode == "launcher-exits" && err != nil {
				t.Fatalf("unexpected check result: %v", err)
			}
			time.Sleep(1100 * time.Millisecond)
			if _, err := os.Stat(leaked); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("shell descendant outlived its check: %v", err)
			}
		})
	}
}
