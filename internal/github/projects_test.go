// SPDX-License-Identifier: MPL-2.0
package github

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProjectProviderFollowsOnlyBoundIssue(t *testing.T) {
	type obj = map[string]any
	const item = "PVTI_example"
	var writes atomic.Int32
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ticket := func(kind, state, repo string, archived bool) obj {
		return obj{"id": item, "isArchived": archived,
			"content":          obj{"__typename": kind, "id": "I_47", "number": 7, "title": "Fix sidebar", "state": state, "repository": obj{"id": "R_31", "nameWithOwner": repo}},
			"fieldValueByName": obj{"name": "Todo", "optionId": "todo"}}
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app":
			fmt.Fprint(w, `{"id":7,"slug":"factory"}`)
		case "/app/installations/22/access_tokens":
			var req struct {
				Permissions map[string]string
				Repos       []int `json:"repository_ids"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Permissions) != 2 || req.Permissions["issues"] != "read" || (req.Permissions["organization_projects"] != "read" && req.Permissions["organization_projects"] != "write") || len(req.Repos) != 1 || req.Repos[0] != 31 {
				t.Error("incorrect token scope")
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(obj{"token": req.Permissions["organization_projects"]})
		case "/repositories/31":
			fmt.Fprint(w, `{"id":31,"node_id":"R_31","full_name":"example/web"}`)
		case "/repos/example/web/issues/7":
			fmt.Fprint(w, `{"id":47,"node_id":"I_47","number":7,"state":"open","title":"Fix sidebar","body":"Private requirements","updated_at":"2026-11-26T10:00:00Z"}`)
		case "/graphql":
			var q struct {
				Query     string
				Variables obj
			}
			if json.NewDecoder(r.Body).Decode(&q) != nil {
				t.Error("invalid GraphQL body")
				return
			}
			n := ticket("Issue", "OPEN", "Example/Web", false)
			n["project"] = obj{"id": "PVT_A", "number": 3, "closed": false, "owner": obj{"__typename": "Organization", "login": "Example"},
				"field": obj{"id": "field", "options": []obj{{"name": "In progress", "id": "active"}}}}
			var data obj
			switch {
			case strings.HasPrefix(q.Query, "query ProjectIssues"):
				if q.Variables["owner"] != "example" || q.Variables["number"] != float64(3) || q.Variables["after"] != "previous" {
					t.Error("incorrect pagination or board")
				}
				nodes := []any{n, ticket("Issue", "OPEN", "other/repo", false), ticket("PullRequest", "OPEN", "example/web", false), ticket("DraftIssue", "", "", false), ticket("Issue", "CLOSED", "example/web", false), ticket("Issue", "OPEN", "example/web", true), nil}
				data = obj{"organization": obj{"projectV2": obj{"number": 3, "title": "Delivery", "closed": false, "items": obj{"nodes": nodes, "pageInfo": obj{"hasNextPage": true, "endCursor": "next"}}}}}
			case strings.HasPrefix(q.Query, "query ProjectImport"), strings.HasPrefix(q.Query, "query ProjectStatusSubject"):
				if q.Variables["item"] != item {
					t.Error("incorrect item")
				}
				if writes.Load() > 0 {
					n["fieldValueByName"] = obj{"optionId": "active"}
				}
				data = obj{"node": n}
			case strings.HasPrefix(q.Query, "mutation ProjectStatus"):
				if r.Header.Get("Authorization") != "Bearer write" || q.Variables["project"] != "PVT_A" || q.Variables["field"] != "field" || q.Variables["option"] != "active" || q.Variables["item"] != item {
					t.Error("mutation scope or subject mismatch")
				}
				writes.Add(1)
				data = obj{"updateProjectV2ItemFieldValue": obj{"projectV2Item": obj{"id": item}}}
			default:
				t.Error("unexpected GraphQL operation")
			}
			json.NewEncoder(w).Encode(obj{"data": data})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	a, err := NewLoopbackFixture("fixture", fixtureKey(t, key, false), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	cursor := "previous"
	page, err := a.ListProjectIssues(ctx, 22, 31, "example/web", 3, &cursor)
	if err != nil || len(page.Issues) != 1 || page.Issues[0].ItemID != item || page.NextCursor == nil || *page.NextCursor != "next" {
		t.Fatal(page, err)
	}
	selected, err := a.ReadProjectIssue(ctx, 22, 31, "example/web", 3, 7, item)
	if err != nil || selected.ProjectNode != "PVT_A" || selected.Issue.Body != "Private requirements" {
		t.Fatal(selected, err)
	}
	for _, pair := range [][2]int32{{4, 7}, {3, 8}} {
		if _, err = a.ReadProjectIssue(ctx, 22, 31, "example/web", pair[0], pair[1], item); !errors.Is(err, ErrSubjectChanged) {
			t.Fatal("foreign import", err)
		}
	}
	write := func(id int64, board, status string) error {
		return a.SetProjectStatus(ctx, 22, 31, "example/web", 7, id, board, item, status)
	}
	if !errors.Is(write(47, "PVT_B", "In Progress"), ErrSubjectChanged) || !errors.Is(write(99, "PVT_A", "In Progress"), ErrSubjectChanged) || write(47, "PVT_A", "Missing") == nil || writes.Load() != 0 {
		t.Fatal("foreign, replaced or unconfigured status accepted")
	}
	for range 2 {
		if err = write(47, "PVT_A", "In Progress"); err != nil {
			t.Fatal(err)
		}
	}
	if writes.Load() != 1 {
		t.Fatal("duplicate mutation", writes.Load())
	}
}
