// SPDX-License-Identifier: MPL-2.0
package journalbridge

import (
	"context"
	"errors"
	"fmt"
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

func TestTypedJournalFollowsReplacementAndRecognizesUpgrade(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runner.sqlite")
	c := testClient(t)
	const id = "11111111111111111111111111111111"
	seed := func(marker string) {
		checked(t, c, path, "INSERT INTO journal_attempts VALUES('"+id+"','one','a/b',1,1,'"+marker+"',1);", "")
	}
	get := func() ([]byte, error) {
		return c.call(t.Context(), path, request{Operation: map[string]any{"get": map[string]string{"id": id}}})
	}
	expect := func(marker string) {
		t.Helper()
		b, e := get()
		if string(b) != marker || e != nil {
			t.Fatalf("read %q, %v; wanted %q", b, e, marker)
		}
	}
	seed("original")
	expect("original")
	if err := os.Rename(path, filepath.Join(dir, "saved.sqlite")); err != nil {
		t.Fatal(err)
	}
	seed("restored")
	expect("restored")
	checked(t, c, path, "PRAGMA user_version=99;", "")
	if _, err := get(); err == nil || !strings.Contains(err.Error(), "compatible Bugsnitch version") {
		t.Fatalf("newer on-disk schema: %v", err)
	}
}

func TestJournalRejectsMisroutedOperations(t *testing.T) {
	c := testClient(t)
	dir := t.TempDir()
	for _, tt := range []struct {
		file, op string
		args     any
	}{
		{"runner.sqlite", "sync_authority", map[string]string{"device": "x"}},
		{"device.sqlite", "summaries", struct{}{}},
		{"runner.sqlite", "save", map[string]any{"id": "x", "version": 1, "query": "a", "claim": false}},
	} {
		_, err := c.call(t.Context(), filepath.Join(dir, tt.file), request{Operation: map[string]any{tt.op: tt.args}})
		if err == nil || !strings.Contains(err.Error(), "protocol incompatible") {
			t.Fatalf("accepted %s on %s: %v", tt.op, tt.file, err)
		}
	}
}

func TestNativeCheckpointsCompeteForFinalWorkspaceSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.sqlite")
	one, two := testClient(t), testClient(t)
	ids := []string{strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)}
	for i, id := range ids {
		state := "waiting"
		if i == 0 {
			state = "running"
		}
		checked(t, one, path, fmt.Sprintf("INSERT INTO journal_attempts VALUES('%s','one','a/b',%d,1,'{\"id\":\"%s\",\"state\":\"%s\",\"version\":1}',1);", id, i+1, id, state), "")
	}
	start := make(chan struct{})
	replies := make(chan string, 2)
	for i, c := range []*client{one, two} {
		go func() {
			<-start
			id := ids[i+1]
			body := map[string]any{"id": id, "version": 1, "claim": true, "data": map[string]any{"id": id, "version": 2, "state": "running"}}
			b, err := c.call(t.Context(), path, request{Operation: map[string]any{"save": body}})
			if err != nil {
				t.Error(err)
			}
			replies <- string(b)
		}()
	}
	close(start)
	a, b := <-replies, <-replies
	if !(a == "1" && b == "0" || a == "0" && b == "1") {
		t.Fatalf("competing claims returned %q and %q", a, b)
	}
	checked(t, one, path, "SELECT count(*) FROM journal_attempts WHERE json_extract(data,'$.state')='running'; SELECT count(*) FROM journal_attempts WHERE version=2;", "2\n1\n")
}
