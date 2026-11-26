// SPDX-License-Identifier: MPL-2.0
package agent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type openAITransport func(*http.Request) (*http.Response, error)

func (f openAITransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenAIStructuredResponse(t *testing.T) {
	const key = "sk-synthetic-fixture-key"
	const model = "gpt-fixture"
	status, responseStatus := http.StatusOK, "completed"
	original := openAIHTTP
	t.Cleanup(func() { openAIHTTP = original })
	openAIHTTP = &http.Client{Transport: openAITransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+key || r.URL.Host != "api.openai.com" {
			t.Fatal("unexpected provider credentials or host")
		}
		raw := `{"data":[{"id":"gpt-fixture"},{"id":"gpt-fixture"},{"id":"sk-synthetic-fixture-key"}]}`
		if r.URL.Path == "/v1/responses" {
			var request struct {
				Model, Instructions, Input string
				Store                      *bool
				Tools                      json.RawMessage
				Text                       struct {
					Format struct {
						Type   string
						Strict bool
					}
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Model != model || request.Input != "provided context" || request.Instructions != "Use no tools" ||
				request.Store == nil || *request.Store || request.Tools != nil ||
				request.Text.Format.Type != "json_schema" || !request.Text.Format.Strict {
				t.Fatal("unexpected Responses request")
			}
			raw = `{"id":"resp_fixture","model":"gpt-fixture-2026","status":"` + responseStatus + `",
			  "usage":{"input_tokens":12,"output_tokens":8},
			  "output":[{"type":"message","content":[{"type":"output_text","text":"{\"summary\":\"reviewed\"}"}]}]}`
		} else if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			t.Fatal("unexpected endpoint")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(raw))}, nil
	})}
	models, err := OpenAIModels(t.Context(), key)
	if err != nil || len(models) != 1 || models[0] != model {
		t.Fatal("unexpected model catalog", err)
	}
	schema := map[string]any{"type": "object"}
	turn := func() (Result, error) {
		return OpenAI(t.Context(), key, "Use no tools", "provided context", model, schema)
	}
	result, err := turn()
	if err != nil || result.Auth != "api-key" || result.Thread != "resp_fixture" || result.Model != "gpt-fixture-2026" ||
		result.Text != `{"summary":"reviewed"}` || result.Usage == nil || result.Usage.OutputTokens != 8 {
		t.Fatal(result, err)
	}
	responseStatus = "incomplete"
	if _, err := turn(); err == nil {
		t.Fatal("incomplete response was accepted")
	}
	status = http.StatusUnauthorized
	if _, err := turn(); !errors.Is(err, OpenAIKeyRequired) {
		t.Fatal("unexpected credential error", err)
	}
}
