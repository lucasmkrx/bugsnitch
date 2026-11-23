// SPDX-License-Identifier: MPL-2.0
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

type verificationHTTP func(*http.Request) (*http.Response, error)

func (f verificationHTTP) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestVerificationCLIImportsStdinAndRecallsReceipt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUGSNITCH_HOME", dir)
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "fixture")
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = verificationHTTP(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/repos/fixture/notes/issues/42" {
			t.Fatal(req.Method, req.URL)
		}
		body := `{"id":42,"number":42,"title":"Notes","state":"open","updated_at":"2026-11-23T09:00:00Z","html_url":"https://github.com/fixture/notes/issues/42","body":"Check the following week."}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	criteria := filepath.Join(dir, "criteria.json")
	if err := os.WriteFile(criteria, []byte(`["Following week is empty"]`), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(dir, "observations")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString(`[{"criterion":"Following week is empty","result":"pending","evidence":"Second device is offline."}]`); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	t.Cleanup(func() { os.Stdin, os.Stdout = stdin, stdout })
	os.Stdin = input
	call := func(args ...string) []byte {
		t.Helper()
		output, err := os.CreateTemp(dir, "output")
		if err != nil {
			t.Fatal(err)
		}
		defer output.Close()
		os.Stdout = output
		if err := run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(output.Name())
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	data := call("local-verify", "--repository", "fixture/notes", "--number", "42", "--criteria", criteria, "--observations", "-", "--source", "check", "--observer", "scripted fixture", "--environment", "fixture")
	var receipt runner.VerificationReceipt
	if err := json.Unmarshal(data, &receipt); err != nil || receipt.Source != "imported-check" || receipt.Observer != "scripted fixture" || receipt.Result != "pending" || receipt.Revision != "" {
		t.Fatal(receipt, err)
	}
	if !bytes.Equal(data, call("local-verification", "--id", receipt.ID)) {
		t.Fatal("saved evidence differed from original CLI output")
	}
}

// Run the real CLI entry point in a child with a synthetic issue transport.
func TestVerificationSIGTERMStopsCheck(t *testing.T) {
	const fixtureKey = "BUGSNITCH_TEST_SIGTERM_VERIFY"
	if dir := os.Getenv(fixtureKey); dir != "" {
		http.DefaultTransport = verificationHTTP(func(req *http.Request) (*http.Response, error) {
			body := `{"id":42,"number":42,"title":"Notes","state":"open","updated_at":"2026-11-23T09:00:00Z","html_url":"https://github.com/fixture/notes/issues/42","body":"test"}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		})
		os.Args = []string{"bugsnitch", "local-verify", "--repository", "fixture/notes", "--number", "42", "--criteria", filepath.Join(dir, "criteria"), "--environment", "fixture", "--revision", "fixture-1", "--", "/bin/sh", "-c", `( sleep 1; touch "$2" ) & echo ready > "$1"; wait`, "fixture", filepath.Join(dir, "ready"), filepath.Join(dir, "late")}
		main()
		return
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "criteria"), []byte(`["fixture"]`), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(run, exe, "-test.run=^TestVerificationSIGTERMStopsCheck$")
	cmd.Env = append(os.Environ(), fixtureKey+"="+dir, "BUGSNITCH_HOME="+dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("fixture exited before starting check: %v %s", err, stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(stderr.String(), "verification check interrupted") {
		t.Fatalf("unclean cancellation: %v %s", err, stderr.String())
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "late")); !os.IsNotExist(err) {
		t.Fatalf("descendant survived: %v", err)
	}
}
