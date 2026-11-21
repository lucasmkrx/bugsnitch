package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestInspectionReadsOnlyAndBindsChecksToExactHead(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	checked := strings.Repeat("b", 40)
	base := strings.Repeat("c", 40)
	count := 3
	app := App{clientID: "fixture", key: key, base: "https://fixture.invalid"}
	app.client = &http.Client{Transport: fixtureTransport(func(req *http.Request) (*http.Response, error) {
		var result any
		status := 200
		switch req.URL.Path {
		case "/app":
			result = map[string]any{"id": 10, "slug": "test"}
		case "/app/installations/9/access_tokens":
			var input struct {
				Permissions map[string]string
				IDs         []int64 `json:"repository_ids"`
			}
			if json.NewDecoder(req.Body).Decode(&input) != nil || len(input.IDs) != 1 || input.IDs[0] != 42 {
				t.Fatal("invalid scope")
			}
			for _, level := range input.Permissions {
				if level != "read" {
					t.Fatal("inspection requested write access")
				}
			}
			result = map[string]string{"token": "fixture-token"}
			status = 201
		case "/repositories/42":
			result = map[string]any{"id": 42, "full_name": "example/fixture"}
		case "/graphql":
			var q struct{ Query string }
			if json.NewDecoder(req.Body).Decode(&q) != nil || strings.Contains(q.Query, "mutation") || !strings.HasPrefix(q.Query, "query ") {
				t.Fatal("inspection requested a mutation")
			}
			result = map[string]any{"data": map[string]any{"repository": map[string]any{"nameWithOwner": "example/fixture", "issue": map[string]any{"number": 3, "id": "canonical", "title": "Ticket", "body": "Must preserve behavior", "state": "OPEN"}, "pullRequest": map[string]any{
				"number": 4, "title": "Fix", "state": "OPEN", "headRefOid": head, "baseRefName": "main", "baseRef": map[string]any{"target": map[string]string{"oid": base}}, "reviewDecision": "APPROVED", "commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"oid": checked, "statusCheckRollup": map[string]any{"state": "SUCCESS", "contexts": map[string]int{"totalCount": count}}}}}},
			}}}}
		default:
			t.Fatalf("unexpected request %s", req.URL)
		}
		body, _ := json.Marshal(result)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	run := func() PullObservation {
		t.Helper()
		o, err := app.InspectPull(context.Background(), 9, 42, "example/fixture", 4, 3)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	if o := run(); o.Checks != "UNKNOWN" || o.CheckCount != 0 {
		t.Fatal("accepted checks on a different revision")
	}
	checked = head
	count = 0
	if o := run(); o.Checks != "UNKNOWN" {
		t.Fatal("missing checks credited")
	}
	count = 3
	if o := run(); o.Checks != "SUCCESS" || o.CheckCount != 3 || o.Head != head || o.Base != base || o.Review != "APPROVED" {
		t.Fatal(o)
	}
}
