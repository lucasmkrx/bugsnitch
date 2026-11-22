package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/lucasmkrx/bugsnitch/internal/testgithub"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestInspectionReadsOnlyAndBindsChecksToExactHead(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	checked := strings.Repeat("b", 40)
	base := strings.Repeat("c", 40)
	count := 3
	allowRules := true
	var mu sync.Mutex
	origin := testgithub.Serve(t, func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
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
		case "/repos/example/fixture/rules/branches/main":
			if req.Method != "GET" {
				t.Fatal("rule inspection requires GET")
			}
			if !allowRules {
				status = 403
				result = map[string]string{"message": "denied"}
				break
			}
			result = []map[string]any{{"type": "merge_queue"}, {"type": "required_status_checks", "parameters": map[string]any{"required_status_checks": []any{map[string]any{"context": "ci", "integration_id": 17}, map[string]any{"context": "loose"}}}}}
		case "/graphql":
			var q struct{ Query string }
			if json.NewDecoder(req.Body).Decode(&q) != nil || strings.Contains(q.Query, "mutation") || !strings.HasPrefix(q.Query, "query ") {
				t.Fatal("inspection requested a mutation")
			}
			result = map[string]any{"data": map[string]any{"repository": map[string]any{"nameWithOwner": "example/fixture", "owner": map[string]string{"__typename": "Organization"}, "issue": map[string]any{"number": 3, "id": "canonical", "title": "Ticket", "body": "Must preserve <behavior> & line\u2028break", "state": "OPEN"}, "pullRequest": map[string]any{
				"mergeQueue": map[string]string{"id": "opaque"}, "mergeQueueEntry": map[string]any{"state": "QUEUED", "position": 1, "headCommit": map[string]string{"oid": head}}, "number": 4, "title": "Fix", "state": "OPEN", "headRefOid": head, "baseRefName": "main", "baseRef": map[string]any{"target": map[string]string{"oid": base}}, "reviewDecision": "APPROVED", "commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"oid": checked, "statusCheckRollup": map[string]any{"state": "SUCCESS", "contexts": map[string]int{"totalCount": count}}}}}},
			}}}}
		default:
			t.Fatalf("unexpected request %s", req.URL)
		}
		body, _ := json.Marshal(result)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	app, err := NewLoopbackFixture("fixture", fixtureKey(t, key, false), origin)
	if err != nil {
		t.Fatal(err)
	}
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
	mu.Lock()
	checked = head
	count = 0
	mu.Unlock()
	if o := run(); o.Checks != "UNKNOWN" {
		t.Fatal("missing checks credited")
	}
	mu.Lock()
	count = 3
	mu.Unlock()
	if o := run(); o.Checks != "SUCCESS" || o.CheckCount != 3 || o.Head != head || o.Base != base || o.Review != "APPROVED" {
		t.Fatal(o)
	}
	obs := run()
	expected, _ := json.Marshal([]string{"canonical", "Ticket", "Must preserve <behavior> & line\u2028break"})
	digest := sha256.Sum256(expected)
	if obs.Criteria != hex.EncodeToString(digest[:]) {
		t.Fatal("previous-release criteria hash changed")
	}
	q := obs.Queue
	if q == nil || !q.Present || q.Rules == nil || !q.Rules.QueueRequired || q.Rules.RequiredChecks != 2 || q.Rules.AppBoundChecks != 1 || q.EntryHead != head {
		t.Fatal("queue evidence incomplete", q)
	}
	// Existing approvals store a digest over the sorted, compact top-level
	// definitions. Nested members retain their order from the provider.
	definitions := []string{`{"type":"merge_queue"}`, `{"parameters":{"required_status_checks":[{"context":"ci","integration_id":17},{"context":"loose"}]},"type":"required_status_checks"}`}
	sort.Strings(definitions)
	rules, _ := json.Marshal(definitions)
	ruleDigest := sha256.Sum256(rules)
	if q.Rules.Digest != hex.EncodeToString(ruleDigest[:]) {
		t.Fatal("previous-release rule digest changed")
	}
	mu.Lock()
	allowRules = false
	mu.Unlock()
	if q := run().Queue; q == nil || q.Rules != nil {
		t.Fatal("unreadable rules were reported as verified", q)
	}
}
