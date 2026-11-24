// SPDX-License-Identifier: MPL-2.0
package device

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
	"io"
	"net/http"
	"testing"
)

func TestMeteringNegotiatesAndReplaysOnlyBoundTurns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUGSNITCH_HOME", dir)
	enrollment := protocol.Enrollment{Workspace: "personal", DeviceID: "fixture-device", Projects: []protocol.Project{{ID: "project1", Repository: "org/repo"}}}
	if err := save(profile{Credential: "fixture", Server: "http://localhost", Enrollment: enrollment}, true); err != nil {
		t.Fatal(err)
	}
	id := insertSyncAttempt(t, dir, "personal", "org/repo", 1, enrollment.DeviceID, 7)
	r := agent.Result{Model: "model-1", Usage: &agent.ModelUsage{InputTokens: 44, OutputTokens: 5}}
	MeasureWorker(runner.Summary{ID: id, Workspace: "personal", Repository: "org/repo"}, r)
	MeasureGeneral(turns.Operation{ID: "general-turn", Workspace: "personal", Device: enrollment.DeviceID}, r)
	MeasureGeneral(turns.Operation{ID: "unrelated", Workspace: "personal", Device: "other-device"}, r)
	prior := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prior })
	var calls int
	lose := true
	http.DefaultTransport = requestRoute(func(req *http.Request) (*http.Response, error) {
		var body protocol.Sync
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if len(body.Metering) > 0 {
			calls++
			if !enrollment.TurnMetering || len(body.Metering) != 2 || body.Metering[0].Subject != id || body.Metering[0].Sequence != 7 || body.Metering[1].Subject != "general-turn" {
				t.Errorf("unexpected negotiated usage %#v", body.Metering)
			}
			if lose {
				return nil, errors.New("fixture lost acknowledgement")
			}
		}
		b, _ := json.Marshal(enrollment)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b))}, nil
	})
	if err := syncOnce(t.Context()); err != nil || calls != 0 {
		t.Fatal("legacy sync", err, calls)
	}
	enrollment.TurnMetering = true
	if err := syncOnce(t.Context()); err == nil || calls != 1 {
		t.Fatal("lost response", err, calls)
	}
	lose = false
	for range 2 {
		if err := syncOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("metering replay", calls)
	}
}
