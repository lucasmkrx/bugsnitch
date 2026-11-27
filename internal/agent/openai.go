// SPDX-License-Identifier: MPL-2.0
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

const OpenAIKeyRequired Failure = "OpenAI API key missing or rejected"

var (
	openAIKey   = regexp.MustCompile(`^sk-[A-Za-z0-9_-]{8,509}$`)
	openAIModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,119}$`)
	openAIHTTP  = &http.Client{
		Timeout: 4 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

func ValidOpenAIKey(key string) bool { return openAIKey.MatchString(key) }
func ValidOpenAIModel(model string) bool {
	return openAIModel.MatchString(model) && !strings.HasPrefix(model, "sk-")
}

func openAIRequest(ctx context.Context, client *http.Client, key, method, endpoint string, request, response any) error {
	if !ValidOpenAIKey(key) {
		return OpenAIKeyRequired
	}
	var body io.Reader
	if request != nil {
		raw, err := json.Marshal(request)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.openai.com/v1/"+endpoint, body)
	if err != nil {
		return Provider
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return Provider
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return OpenAIKeyRequired
	case http.StatusTooManyRequests:
		return RateLimit
	case http.StatusNotFound:
		return UnavailableModel
	case http.StatusBadRequest:
		return errors.Join(UnavailableModel, errors.New("Check model and structured output support"))
	default:
		return Provider
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if dec.Decode(response) != nil || dec.Decode(new(any)) != io.EOF {
		return Provider
	}
	return nil
}

func OpenAIModels(ctx context.Context, key string) ([]string, error) {
	var response struct {
		Data []struct{ ID string }
	}
	if err := openAIRequest(ctx, openAIHTTP, key, http.MethodGet, "models", nil, &response); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(response.Data))
	for _, model := range response.Data {
		if ValidOpenAIModel(model.ID) && model.ID != key {
			models = append(models, model.ID)
		}
	}
	slices.Sort(models)
	return slices.Compact(models), nil
}

func OpenAI(ctx context.Context, key, directive, prompt, model string, schema any) (Result, error) {
	if !ValidOpenAIModel(model) {
		return Result{}, UnavailableModel
	}
	request := map[string]any{
		"model": model, "instructions": directive, "input": prompt,
		"store": false,
	}
	if schema != nil {
		request["text"] = map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "bugsnitch_result", "strict": true, "schema": schema,
		}}
	}
	var response struct {
		ID, Model, Status string
		Usage             *struct {
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
		}
		Output []struct {
			Type    string
			Content []struct{ Type, Text string }
		}
	}
	if err := openAIRequest(ctx, openAIHTTP, key, http.MethodPost, "responses", request, &response); err != nil {
		return Result{}, err
	}
	if response.ID == "" || !ValidOpenAIModel(response.Model) || strings.Contains(response.ID, key) {
		return Result{}, Provider
	}
	result := Result{Thread: response.ID, Turn: response.ID, Model: response.Model, Auth: "api-key"}
	if u := response.Usage; u != nil && u.Input != nil && u.Output != nil && *u.Input >= 0 && *u.Input <= 1<<32 && *u.Output >= 0 && *u.Output <= 1<<32 {
		result.Usage = &ModelUsage{InputTokens: *u.Input, OutputTokens: *u.Output}
	}
	if response.Status != "completed" {
		return result, errors.New("OpenAI did not complete the model response")
	}
	var answer strings.Builder
	for _, item := range response.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "refusal" {
				return result, errors.New("OpenAI declined the model request")
			}
			if content.Type == "output_text" {
				answer.WriteString(content.Text)
			}
		}
	}
	raw := answer.String()
	if strings.TrimSpace(raw) == "" || strings.Contains(raw, key) || schema != nil && !json.Valid([]byte(raw)) {
		return result, errors.New("OpenAI returned no usable text or structured output")
	}
	result.Text = raw
	return result, nil
}
