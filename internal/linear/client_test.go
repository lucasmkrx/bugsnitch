// SPDX-License-Identifier: MPL-2.0
package linear

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIssueAcceptsBoundSnapshotAndRejectsPartialError(t *testing.T) {
	client := New("fixture-key")
	failure := ""
	client.http.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "fixture-key" || r.URL.String() != "https://api.linear.app/graphql" {
			t.Fatal("unexpected Linear request")
		}
		var payload struct {
			Query     string
			Variables map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Variables["id"] != "DEMO-12" ||
			!strings.Contains(payload.Query, "issue(id: $id)") || strings.Contains(payload.Query, "mutation") {
			t.Fatal("unexpected query", err)
		}
		body := `{"data":{"issue":{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d","identifier":"DEMO-12",
		  "title":"Show pickup time","url":"https://linear.app/demo/issue/DEMO-12/show-pickup-time",
		  "updatedAt":"2026-11-24T00:00:00Z","state":{"name":"In Progress","type":"started"}}}` + failure + `}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	issue, err := client.Issue(t.Context(), "DEMO-12")
	if err != nil || issue.State.Type != "started" {
		t.Fatal(issue, err)
	}
	failure = `,"errors":[{"message":"partial result"}]`
	if _, err := client.Issue(t.Context(), "DEMO-12"); !errors.Is(err, Unavailable) {
		t.Fatal("accepted partial result", err)
	}
}

func TestActivityReconcilesOnlyMatchingLinearSession(t *testing.T) {
	c := New("Bearer fixture")
	const session = "22222222-2222-4222-8222-222222222222"
	const id = "11111111-1111-4111-8111-111111111111"
	resultSession := session
	c.http.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Query     string
			Variables map[string]any
		}
		if e := json.NewDecoder(r.Body).Decode(&payload); e != nil {
			t.Fatal(e)
		}
		result := `{"errors":[{"message":"duplicate activity"}]}`
		if strings.HasPrefix(payload.Query, "query") {
			result = `{"data":{"agentActivity":{"id":"` + id + `","agentSession":{"id":"` + resultSession + `"},"content":{"type":"thought","body":"queued"}}}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(result))}, nil
	})
	if e := c.Activity(t.Context(), session, id, "thought", "queued"); e != nil {
		t.Fatal(e)
	}
	resultSession = id
	if e := c.Activity(t.Context(), session, id, "thought", "queued"); e == nil {
		t.Fatal("accepted another session's activity")
	}
}
