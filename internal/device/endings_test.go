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
	"time"

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

func TestCancelledStartFencesUncertainGrant(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	a := runner.Summary{ID: strings.Repeat("a", 32), Workspace: "personal", Repository: "fixture/repo", LocalPath: "/fixture", Version: 3, State: "waiting", BaseSHA: strings.Repeat("1", 40), IssueNumber: 1}
	p := profile{Server: "http://localhost:8787", Enrollment: protocol.Enrollment{Workspace: a.Workspace, DeviceID: "device", Projects: []protocol.Project{{ID: "project", Repository: a.Repository}}}}
	if err := save(p, true); err != nil {
		t.Fatal(err)
	}
	if _, err := journal("INSERT INTO mappings VALUES('project','/fixture'); INSERT INTO authority VALUES(" + sqlValue(a.ID) + ",'device',1,'');"); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls []string
	http.DefaultTransport = requestRoute(func(r *http.Request) (*http.Response, error) {
		var req protocol.Admission
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		calls = append(calls, fmt.Sprintf("%s/%d", req.Operation, req.Sequence))
		if req.Operation == "turn_start" {
			cancel()
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		if len(calls) == 2 {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"sequence":%d}`, req.Sequence)))}, nil
	})
	if err := TurnStart(ctx, a); !errors.Is(err, runner.ErrAuthorityUnreachable) {
		t.Fatalf("start: %v", err)
	}
	if err := Finish(a); err == nil {
		t.Fatal("lost abandonment acknowledgment")
	}
	if err := completeEnding(a.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"turn_start/2", "turn_abandon/3", "turn_abandon/3", "finish/4"}) {
		t.Fatal(calls)
	}
}

func TestSyncWatchStopsDuringPollingDelay(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	p := profile{Server: "http://localhost:8787", Enrollment: protocol.Enrollment{Workspace: "personal", DeviceID: "fixture"}}
	if err := save(p, true); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	polled := make(chan struct{})
	http.DefaultTransport = requestRoute(func(r *http.Request) (*http.Response, error) {
		close(polled)
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunCLIContext(ctx, []string{"sync", "--watch"}) }()
	select {
	case <-polled:
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not poll")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled watch: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watch stayed active")
	}
}
