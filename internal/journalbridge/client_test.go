// SPDX-License-Identifier: MPL-2.0
package journalbridge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testClient(t *testing.T) *client {
	t.Helper()
	c := &client{gate: make(chan struct{}, 1)}
	t.Cleanup(c.stop)
	return c
}

func checked(t *testing.T, c *client, file, sql, expected string) {
	t.Helper()
	output, err := c.query(t.Context(), file, sql)
	if err != nil || string(output) != expected {
		t.Fatalf("journal result %q, error %v; want %q", output, err, expected)
	}
}

func TestReuseAndIsolatedBatches(t *testing.T) {
	c := testClient(t)
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner.sqlite")
	device := filepath.Join(dir, "device.sqlite")
	checked(t, c, runner, "PRAGMA foreign_keys; PRAGMA busy_timeout; CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER REFERENCES parent); INSERT INTO parent VALUES(1); SELECT changes();", "1\n5000\n1\n")
	pid := c.cmd.Process.Pid
	checked(t, c, device, "PRAGMA foreign_keys; SELECT NULL,42,'a|b',json_array(1,2); SELECT 'line one'||char(10)||'line two';", "0\n|42|a|b|[1,2]\nline one\nline two\n")
	data, err := c.query(t.Context(), runner, "BEGIN IMMEDIATE; INSERT INTO parent VALUES(2); INSERT INTO child VALUES(99); COMMIT;")
	if err == nil || !strings.Contains(err.Error(), "constraint") || len(data) != 0 {
		t.Fatalf("expected a constraint failure, got %q, %v", data, err)
	}
	checked(t, c, runner, "SELECT count(*) FROM parent;", "1\n")
	checked(t, c, runner, "BEGIN IMMEDIATE; INSERT INTO parent VALUES(2);", "")
	checked(t, c, runner, "SELECT count(*) FROM parent;", "1\n")
	if pid != c.cmd.Process.Pid {
		t.Fatal("ordinary journal batches launched another helper")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(device)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("device credential file must remain private")
		}
	}
}

func TestLegacyCLIFileAndListOutput(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("system sqlite3 is required only to manufacture this legacy input")
	}
	path := filepath.Join(t.TempDir(), "runner.sqlite")
	read := "SELECT id,value,NULL,3.5 FROM old ORDER BY id;"
	cmd := exec.Command(sqlite, "-batch", "-bail", "-init", os.DevNull, path,
		"PRAGMA user_version=1; CREATE TABLE old(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO old VALUES(1,'München'),(2,'a|b'); "+read)
	want, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	c := testClient(t)
	checked(t, c, path, read, string(want))
	checked(t, c, path, "PRAGMA user_version; UPDATE old SET value='preserved' WHERE id=1; SELECT changes();", "2\n1\n")
	after, err := exec.Command(sqlite, "-batch", "-bail", "-init", os.DevNull, path,
		"SELECT value FROM old WHERE id=1;").Output()
	if err != nil || string(after) != "preserved\n" {
		t.Fatalf("legacy reader returned %q, %v", after, err)
	}
}

func TestIndependentHelpersContendForOneClaim(t *testing.T) {
	file := filepath.Join(t.TempDir(), "runner.sqlite")
	one, two := testClient(t), testClient(t)
	checked(t, one, file, "CREATE TABLE claims(issue INTEGER PRIMARY KEY);", "")
	var wait sync.WaitGroup
	start := make(chan struct{})
	replies := make(chan string, 2)
	for _, c := range []*client{one, two} {
		wait.Go(func() {
			<-start
			out, err := c.query(t.Context(), file, "BEGIN IMMEDIATE; INSERT OR IGNORE INTO claims VALUES(7); SELECT changes(); COMMIT;")
			if err != nil {
				t.Error(err)
				return
			}
			replies <- string(out)
		})
	}
	close(start)
	wait.Wait()
	close(replies)
	count := map[string]int{}
	for answer := range replies {
		count[answer]++
	}
	if count["0\n"] != 1 || count["1\n"] != 1 {
		t.Fatalf("expected one winning claim, got %v", count)
	}
}

func TestInterruptedReplyDoesNotReplayCommittedWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "runner.sqlite")
	one, observer := testClient(t), testClient(t)
	checked(t, one, file, "CREATE TABLE receipt(n INTEGER); INSERT INTO receipt VALUES(0);", "")
	pid := one.cmd.Process.Pid
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan struct{})
	var interrupted error
	go func() {
		defer close(done)
		// The autocommit completes before a long read holds its reply open.
		_, interrupted = one.query(ctx, file, "UPDATE receipt SET n=n+1; WITH RECURSIVE s(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM s WHERE x<1000000000) SELECT sum(x) FROM s;")
	}()
	defer func() { cancel(); <-done }()
	for {
		out, err := observer.query(ctx, file, "SELECT n FROM receipt;")
		if err != nil {
			t.Fatal(err)
		}
		if string(out) == "1\n" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if !errors.Is(interrupted, context.Canceled) || !strings.Contains(interrupted.Error(), "outcome unknown") {
		t.Fatalf("interrupted request returned %v", interrupted)
	}
	checked(t, one, file, "SELECT n FROM receipt;", "1\n")
	if pid == one.cmd.Process.Pid {
		t.Fatal("cancelled helper was reused")
	}
}
