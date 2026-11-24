// SPDX-License-Identifier: MPL-2.0
package device

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func TestFinishedPublicationRequiresCurrentPairedAuthority(t *testing.T) {
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	a := runner.Summary{ID: "attempt", Workspace: "personal", Repository: "fixture/app", LocalPath: "/selected"}
	if err := CheckPublication(t.Context(), a); err != nil {
		t.Fatal("standalone attempt", err)
	}
	a.Scope = &protocol.RunScope{Project: "project", Device: "device", Plan: "plan", Policy: 2}
	if err := CheckPublication(t.Context(), a); !errors.Is(err, runner.ErrDeviceUnauthorized) {
		t.Fatal("unbound scope accepted", err)
	}
	pr := protocol.Project{ID: "project", Repository: a.Repository, PlanID: "plan", PolicyVersion: 2}
	live := protocol.Enrollment{Workspace: a.Workspace, DeviceID: "device", Projects: []protocol.Project{pr}}
	if err := save(profile{Server: "http://localhost:8787", Enrollment: live}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := journal("INSERT INTO mappings VALUES('project','/selected'); INSERT INTO authority(attempt,device,acked) VALUES('attempt','device',3);"); err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = requestRoute(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/device/sync" {
			t.Errorf("unexpected publication endpoint %s", r.URL.Path)
		}
		b, _ := json.Marshal(live)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})
	if err := CheckPublication(t.Context(), a); err != nil {
		t.Fatal("current assignment", err)
	}
	live.Projects[0].Paused = true
	if err := CheckPublication(t.Context(), a); !errors.Is(err, runner.ErrAuthorityStale) {
		t.Fatal("paused project accepted", err)
	}
	live.Projects[0] = pr
	live.Projects[0].PolicyVersion++
	if err := CheckPublication(t.Context(), a); !errors.Is(err, runner.ErrAuthorityStale) {
		t.Fatal("changed policy accepted", err)
	}
	live.Projects = nil
	if err := CheckPublication(t.Context(), a); !errors.Is(err, runner.ErrDeviceUnauthorized) {
		t.Fatal("removed project accepted", err)
	}
}
