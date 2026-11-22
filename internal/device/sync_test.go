// SPDX-License-Identifier: MPL-2.0
package device

import (
	"bytes"
	"encoding/json"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"io"
	"net/http"
	"testing"
)

func TestSyncRequiresMappingWorkspaceAndAcknowledgedDevice(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUGSNITCH_HOME", dir)
	enrollment := protocol.Enrollment{Workspace: "one", DeviceID: "this-device", Projects: []protocol.Project{{ID: "selected", Repository: "OWNER/project"}, {ID: "unmapped", Repository: "owner/unmapped"}}}
	if err := save(profile{Credential: "example", Server: "http://localhost", Enrollment: enrollment}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := journal("INSERT INTO mappings VALUES('selected','/selected'),('unassigned','/unassigned');"); err != nil {
		t.Fatal(err)
	}
	want := insertSyncAttempt(t, dir, "one", "owner/project", 1, "this-device", 1)
	insertSyncAttempt(t, dir, "one", "owner/project", 2, "another-device", 1)
	insertSyncAttempt(t, dir, "one", "owner/project", 3, "this-device", 0)
	insertSyncAttempt(t, dir, "one", "owner/project", 4, "", 0)
	insertSyncAttempt(t, dir, "two", "owner/project", 5, "this-device", 1)
	insertSyncAttempt(t, dir, "one", "owner/unmapped", 6, "this-device", 1)
	insertSyncAttempt(t, dir, "one", "owner/unassigned", 7, "this-device", 1)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	answer, err := json.Marshal(enrollment)
	if err != nil {
		t.Fatal(err)
	}
	var reported []protocol.Attempt
	http.DefaultTransport = requestRoute(func(r *http.Request) (*http.Response, error) {
		var input protocol.Sync
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			return nil, err
		}
		reported = append(reported, input.Attempts...)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(answer))}, nil
	})
	if err := syncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || reported[0].ID != want || reported[0].ProjectID != "selected" {
		t.Fatalf("unexpected sync projection: %+v", reported)
	}
	if _, err := loadSyncAuthority(t.Context(), protocol.Enrollment{Workspace: "one", DeviceID: "another-device"}); err == nil {
		t.Fatal("read another device's authority under the current enrollment")
	}
	for _, scope := range []protocol.Enrollment{{Workspace: "two", DeviceID: enrollment.DeviceID}, {Workspace: enrollment.Workspace, DeviceID: "other"}} {
		reported = nil
		answer, err = json.Marshal(scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := syncOnce(t.Context()); err == nil || len(reported) != 0 {
			t.Fatal("sync accepted a different device or workspace", err, reported)
		}
	}
}
