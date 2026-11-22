// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestEventCursorFollowsCommittedWorkspaceWrites(t *testing.T) {
	s := fixture(t)
	c := login(t, s)
	if status, _ := (system{handler: c.handler}).req(t, "GET", "/api/v1/events", nil, ""); status != 401 {
		t.Fatalf("anonymous replay returned %d", status)
	}
	for _, query := range []string{"?after=-1", "?after=abc", "?after=9223372036854775808", "?limit=0", "?limit=151"} {
		if status, _ := c.req(t, "GET", "/api/v1/events"+query, nil, ""); status != 400 {
			t.Fatalf("query %s returned %d", query, status)
		}
	}
	page := func(query string) object {
		t.Helper()
		status, result := c.req(t, "GET", "/api/v1/events"+query, nil, "")
		if status != 200 {
			t.Fatalf("event page returned %d", status)
		}
		return result
	}
	_, err := s.DB.Exec("INSERT INTO workspaces(id,account_id,name) VALUES ('foreign','owner','Other')")
	if err != nil {
		t.Fatal(err)
	}
	const insert = "INSERT INTO events(workspace_id,kind,subject_id) VALUES ($1,'test.written',$2)"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback()
	// Allocate the older sequence first, then hold its transaction open while
	// another writer in the same workspace commits and a consumer advances.
	if _, err = first.ExecContext(ctx, insert, "personal", "later"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(ctx, insert, "personal", "committed"); err != nil {
		t.Fatal("concurrent write blocked", err)
	}
	if _, err = s.DB.ExecContext(ctx, insert, "foreign", "private"); err != nil {
		t.Fatal(err)
	}
	before := page("?workspace=foreign")
	visible := before["events"].([]any)
	if len(visible) != 1 || visible[0].(map[string]any)["subjectId"] != "committed" || before["nextCursor"] != "1" {
		t.Fatal("uncommitted or foreign event visible", before)
	}
	aborted, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer aborted.Rollback()
	if _, err = aborted.ExecContext(ctx, insert, "personal", "rolled_back"); err != nil {
		t.Fatal(err)
	}
	if err = aborted.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = first.Commit(); err != nil {
		t.Fatal(err)
	}
	a := page("?limit=1")
	b := page("?limit=1&after=1")
	item := func(p object) map[string]any {
		t.Helper()
		events := p["events"].([]any)
		if len(events) != 1 {
			t.Fatalf("unexpected page %v", p)
		}
		return events[0].(map[string]any)
	}
	if item(a)["seq"].(float64) <= item(b)["seq"].(float64) {
		t.Fatal("fixture did not reverse sequence and commit order")
	}
	if a["hasMore"] != true || a["nextCursor"] != "1" || item(a)["subjectId"] != "committed" || b["hasMore"] != false || b["nextCursor"] != "2" || item(b)["subjectId"] != "later" {
		t.Fatal(a, b)
	}
	empty := page("?after=2")
	if len(empty["events"].([]any)) != 0 || empty["nextCursor"] != "2" || empty["hasMore"] != false {
		t.Fatal(empty)
	}
	status, state := c.req(t, "GET", "/api/v1/state", nil, "")
	if status != 200 || !reflect.DeepEqual(state["events"], []any{item(b), item(a)}) {
		t.Fatal("dashboard snapshot and replay disagree")
	}
}
