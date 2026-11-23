// SPDX-License-Identifier: MPL-2.0
package device

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func TestTurnStartClassifiesCapacityConflict(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	a := runner.Summary{ID: "attempt", Workspace: "personal", Repository: "fixture/app", LocalPath: "/selected"}
	p := profile{Server: "http://localhost:8787", Enrollment: protocol.Enrollment{Workspace: a.Workspace, DeviceID: "device", Projects: []protocol.Project{{ID: "project", Repository: a.Repository}}}}
	if err := save(p, true); err != nil {
		t.Fatal(err)
	}
	if _, err := journal("INSERT INTO mappings VALUES('project','/selected');"); err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, tc := range []struct {
		name, body string
		capacity   bool
	}{
		{"coded", `{"code":"model_turn_capacity","error":"Occupied"}`, true},
		{"legacy", `{"error":"Model-turn capacity is reserved"}`, true},
		{"different code", `{"code":"authority_changed","error":"Model-turn capacity is reserved"}`, false},
		{"empty code", `{"code":"","error":"Model-turn capacity is reserved"}`, false},
		{"null code", `{"code":null,"error":"Model-turn capacity is reserved"}`, false},
		{"different message", `{"error":"Different admission"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			http.DefaultTransport = requestRoute(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 409, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			err := TurnStart(a)
			if !errors.Is(err, statusError(409)) || errors.Is(err, runner.ErrHostedTurnCapacity) != tc.capacity || errors.Is(err, runner.ErrAuthorityStale) == tc.capacity {
				t.Fatalf("unexpected admission classification: %v", err)
			}
			if row, err := journal("SELECT count(*) FROM authority WHERE attempt='attempt' AND pending='' AND acked=0;"); err != nil || row != "1" {
				t.Fatalf("rejected turn changed the admission cursor: %s, %v", row, err)
			}
		})
	}
}
