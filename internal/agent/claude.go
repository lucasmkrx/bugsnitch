// SPDX-License-Identifier: MPL-2.0
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

const claudeMessages = "https://api.anthropic.com/v1/messages"

type ModelUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Claude sends the selected context in a stateless request with no tools.
func Claude(ctx context.Context, directive, prompt, model, effort string, schema any) (Result, error) {
	key := os.Getenv("BUGSNITCH_ANTHROPIC_API_KEY")
	if strings.TrimSpace(key) == "" {
		return Result{}, ClaudeKeyRequired
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return claudeTurn(ctx, client, key, directive, prompt, model, effort, schema)
}

func claudeTurn(ctx context.Context, client *http.Client, key, directive, prompt, model, effort string, schema any) (Result, error) {
	if model == "" {
		return Result{}, UnavailableModel
	}
	request := map[string]any{
		"model": model, "max_tokens": 32_768, "system": directive,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	}
	output := map[string]any{}
	if schema != nil {
		output["format"] = map[string]any{"type": "json_schema", "schema": schema}
	}
	if effort != "" {
		output["effort"] = effort
	}
	if len(output) != 0 {
		request["output_config"] = output
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeMessages, bytes.NewReader(raw))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2026-10-21")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, Provider
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return Result{}, ClaudeKeyRequired
	case http.StatusTooManyRequests:
		return Result{}, RateLimit
	case http.StatusNotFound:
		return Result{}, UnavailableModel
	case http.StatusBadRequest:
		return Result{}, errors.New("Claude rejected the request; check model, effort and structured output support")
	default:
		return Result{}, Provider
	}
	var response struct {
		ID         string
		Model      string
		StopReason string `json:"stop_reason"`
		Usage      *struct {
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
		}
		Content []struct{ Type, Text string }
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if dec.Decode(&response) != nil || dec.Decode(new(any)) != io.EOF ||
		response.ID == "" || response.Model == "" {
		return Result{}, errors.New("Claude did not complete a usable response")
	}
	result := Result{Thread: response.ID, Turn: response.ID, Model: response.Model}
	if u := response.Usage; u != nil && u.Input != nil && u.Output != nil && *u.Input >= 0 && *u.Input <= 1<<32 && *u.Output >= 0 && *u.Output <= 1<<32 {
		result.Usage = &ModelUsage{InputTokens: *u.Input, OutputTokens: *u.Output}
	}
	if response.StopReason != "end_turn" {
		return result, errors.New("Claude did not complete a usable response")
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	answer := text.String()
	if strings.TrimSpace(answer) == "" || (schema != nil && !json.Valid([]byte(answer))) {
		return result, errors.New("Claude returned no usable text or structured output")
	}
	result.Text = answer
	return result, nil
}
