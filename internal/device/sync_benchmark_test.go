// SPDX-License-Identifier: MPL-2.0
package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/journalbridge"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

// Use the actual device sync and both real journals, with the service response
// provided in-process so the measurement excludes network and server variance.
func BenchmarkJournalDeviceSync(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("BUGSNITCH_HOME", dir)
	enrollment := protocol.Enrollment{Workspace: "personal", DeviceID: "bench-device", Projects: []protocol.Project{{ID: "selected", Repository: "owner/project"}}}
	if err := save(profile{Credential: "benchmark", Server: "http://localhost", Enrollment: enrollment}, true); err != nil {
		b.Fatal(err)
	}
	if _, err := journal("INSERT INTO mappings VALUES('selected','/local');"); err != nil {
		b.Fatal(err)
	}
	for n := 1; n <= 32; n++ {
		insertSyncAttempt(b, dir, "personal", "owner/project", n, "bench-device", 1)
	}
	answer, err := json.Marshal(enrollment)
	if err != nil {
		b.Fatal(err)
	}
	previous := http.DefaultTransport
	b.Cleanup(func() { http.DefaultTransport = previous })
	reported := 0
	http.DefaultTransport = requestRoute(func(r *http.Request) (*http.Response, error) {
		var input protocol.Sync
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			return nil, err
		}
		reported += len(input.Attempts)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(answer))}, nil
	})
	b.ReportAllocs()
	for b.Loop() {
		reported = 0
		if err := syncOnce(context.Background()); err != nil || reported != 32 {
			b.Fatalf("reported %d attempts, error %v", reported, err)
		}
	}
}

func insertSyncAttempt(t testing.TB, dir, workspace, repo string, n int, device string, acked int) string {
	t.Helper()
	id := fmt.Sprintf("%032x", n)
	a := runner.Attempt{Summary: runner.Summary{ID: id, Workspace: workspace, Repository: repo, IssueNumber: n, Version: 1, State: "waiting", Phase: "validate", BaseSHA: strings.Repeat("f", 40), Evidence: []runner.StageEvidence{{Phase: "plan"}, {Phase: "implement"}}}}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journalbridge.Query(t.Context(), filepath.Join(dir, "runner.sqlite"), fmt.Sprintf("INSERT INTO journal_attempts VALUES(%s,%s,%s,%d,1,%s,1);", sqlValue(id), sqlValue(workspace), sqlValue(repo), n, sqlValue(string(data)))); err != nil {
		t.Fatal(err)
	}
	if device != "" {
		if _, err = journal(fmt.Sprintf("INSERT INTO authority VALUES(%s,%s,%d,'');", sqlValue(id), sqlValue(device), acked)); err != nil {
			t.Fatal(err)
		}
	}
	return id
}
