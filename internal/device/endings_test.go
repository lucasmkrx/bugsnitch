// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

type requestRoute func(*http.Request) (*http.Response, error)

func (f requestRoute) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLostWorkerEndReplaysOnlyObservedQuiescence(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	deviceID := strings.Repeat("d", 32)
	project := strings.Repeat("1", 32)
	a := runner.Summary{ID: strings.Repeat("a", 32), Repository: "fixture/repo", BaseSHA: strings.Repeat("b", 40), IssueNumber: 7, Workspace: "personal", Version: 4, State: "waiting"}
	p := profile{Credential: strings.Repeat("c", 64), Server: "http://localhost:8787", Enrollment: protocol.Enrollment{DeviceID: deviceID, Workspace: a.Workspace, Projects: []protocol.Project{{ID: project, Repository: a.Repository}}}}
	if err := save(p, true); err != nil {
		t.Fatal(err)
	}
	pending, err := json.Marshal(protocol.Admission{Protocol: protocol.Version, Operation: "turn_end", Sequence: 3, ProjectID: project, AttemptID: a.ID, IssueNumber: a.IssueNumber, BaseSHA: a.BaseSHA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal("INSERT INTO mappings VALUES(" + sqlValue(project) + ",'/selected'); INSERT INTO authority VALUES(" + sqlValue(a.ID) + "," + sqlValue(deviceID) + ",2," + sqlValue(string(pending)) + ");"); err != nil {
		t.Fatal(err)
	}
	var history []string
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = requestRoute(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/device/admission" {
			return nil, errors.New("unexpected route")
		}
		var req protocol.Admission
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		history = append(history, fmt.Sprintf("%s/%d", req.Operation, req.Sequence))
		if len(history) == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"sequence":%d}`, req.Sequence)))}, nil
	})
	if err = Finish(a); err == nil {
		t.Fatal("premature completion acknowledgment")
	}
	if n, err := journal("SELECT count(*) FROM worker_endings;"); err != nil || n != "1" {
		t.Fatal("end observation lost", n, err)
	}
	if err = completeEnding(a.ID); err != nil {
		t.Fatal(err)
	}
	if err = completeEnding(a.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(history, []string{"turn_end/3", "turn_end/3", "finish/4"}) {
		t.Fatal(history)
	}
	if n, err := journal("SELECT count(*) FROM worker_endings;"); err != nil || n != "0" {
		t.Fatal("confirmed end still pending", n, err)
	}
	if _, err = journal("DELETE FROM profile;"); err != nil {
		t.Fatal(err)
	}
	if _, err = CheckWorker(context.Background(), a, false); !errors.Is(err, runner.ErrDeviceUnauthorized) {
		t.Fatal("unpair silently downgraded an admitted worker", err)
	}
}
