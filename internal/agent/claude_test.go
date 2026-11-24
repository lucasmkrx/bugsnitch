// SPDX-License-Identifier: MPL-2.0
package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type claudeTransport func(*http.Request) (*http.Response, error)

func (f claudeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClaudeStructuredTurnAndTruncation(t *testing.T) {
	stop := "end_turn"
	c := &http.Client{Transport: claudeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != claudeMessages || r.Header.Get("x-api-key") != "fixture-key" {
			t.Fatal("unexpected provider request")
		}
		var b map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Fatal(err)
		}
		if b["tools"] != nil || !strings.Contains(string(b["output_config"]), "json_schema") || string(b["system"]) != `"Use no tools"` {
			t.Fatal("missing structured response contract or tool-free directive")
		}
		raw := `{"id":"msg_fixture","model":"claude-fixture","stop_reason":"` + stop + `",
		  "usage":{"input_tokens":36,"output_tokens":17},
		  "content":[{"type":"text","text":"{\"summary\":\"Reviewed\"}"}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))}, nil
	})}
	schema := map[string]any{"type": "object"}
	r, err := claudeTurn(t.Context(), c, "fixture-key", "Use no tools", "selected context", "claude-fixture", "", schema)
	if err != nil || r.Thread != "msg_fixture" || r.Usage == nil || r.Usage.OutputTokens != 17 {
		t.Fatal(r, err)
	}
	stop = "max_tokens"
	if _, err := claudeTurn(t.Context(), c, "fixture-key", "Use no tools", "selected context", "claude-fixture", "", schema); err == nil {
		t.Fatal("truncated turn was accepted")
	}
}
