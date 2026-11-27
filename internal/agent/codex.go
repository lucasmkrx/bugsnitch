// Package agent runs user-initiated local Codex turns over private stdio pipes.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const Version = "0.171.0-alpha.13"

type Model struct {
	ID                        string `json:"id"`
	Model                     string `json:"model"`
	IsDefault                 bool   `json:"isDefault"`
	DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []struct {
		ReasoningEffort string `json:"reasoningEffort"`
	} `json:"supportedReasoningEfforts"`
}

type Message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Params json.RawMessage `json:"params"`
}

type Client struct {
	threadConfig map[string]any
	cmd          *exec.Cmd
	in           io.WriteCloser
	out          *bufio.Scanner
	dir          string
	next         int
}
type Result struct {
	Usage      *ModelUsage `json:"usage,omitempty"`
	DurationMS *int64      `json:"-"`
	Auth       string      `json:"-"`
	Model      string      `json:"model"`
	Thread     string      `json:"thread"`
	Turn       string      `json:"turn"`
	Text       string      `json:"text"`
}

type Failure string

func (f Failure) Error() string { return string(f) }

const (
	SignIn            Failure = "ChatGPT sign-in required"
	ClaudeKeyRequired Failure = "Claude API key missing or rejected"
	Allowance         Failure = "ChatGPT usage allowance reached"
	RateLimit         Failure = "provider rate limit reached"
	UnavailableModel  Failure = "selected model unavailable"
	Provider          Failure = "provider connection or service unavailable"
	Interrupted       Failure = "model turn interrupted"
)

func localEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "LANG", "USER", "CODEX_HOME", "XDG_RUNTIME_DIR", "SYSTEMROOT"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Open isolates configuration lookup from the candidate checkout. No provider keys,
// Git credentials, or service credentials are inherited from the caller's environment.
func Open(ctx context.Context) (*Client, error) {
	version := exec.CommandContext(ctx, "codex", "--version")
	version.Env = localEnv()
	v, err := version.Output()
	if err != nil {
		return nil, errors.New("Codex executable unavailable")
	}
	if strings.TrimSpace(string(v)) != "codex-cli "+Version {
		return nil, fmt.Errorf("unsupported Codex version; this runner requires %s", Version)
	}
	dir, err := os.MkdirTemp("", "bugsnitch-turn-")
	if err != nil {
		return nil, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	args := []string{"app-server", "--listen", "stdio://", "-c", `forced_login_method="chatgpt"`, "-c", `model_provider="openai"`, "-c", `web_search="disabled"`, "-c", `shell_environment_policy.inherit="none"`, "-c", "mcp_servers={}", "-c", "analytics.enabled=false"}
	for _, flag := range []string{"apps", "plugins", "hooks", "shell_tool", "browser_use", "computer_use", "image_generation", "view_image", "multi_agent", "multi_agent_v2", "memories", "code_mode", "in_app_browser"} {
		args = append(args, "--disable", flag)
	}
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = dir
	cmd.Env = localEnv()
	cmd.WaitDelay = time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		os.Remove(dir)
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		os.Remove(dir)
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		os.Remove(dir)
		return nil, errors.New("unable to launch Codex app-server")
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	c := &Client{cmd: cmd, in: in, out: scanner, dir: dir}
	fail := func(e error) (*Client, error) { c.Close(); return nil, e }
	err = c.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "bugsnitch", "title": "Bugsnitch", "version": "0.1.0"}}, nil)
	if err != nil {
		return fail(err)
	}
	if err = c.send(map[string]any{"method": "initialized"}); err != nil {
		return fail(err)
	}
	var acct struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if err = c.call("account/read", map[string]bool{"refreshToken": false}, &acct); err != nil {
		return fail(err)
	}
	if acct.Account == nil || acct.Account.Type != "chatgpt" {
		return fail(SignIn)
	}
	// Global registrations include disabled servers. Construct a per-thread override,
	// then verify the effective thread inventory before sending any model input.
	disabled := map[string]any{}
	for cursor := ""; ; {
		var page struct {
			Data []struct {
				Name string `json:"name"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		params := map[string]any{"detail": "toolsAndAuthOnly", "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err = c.call("mcpServerStatus/list", params, &page); err != nil {
			return fail(err)
		}
		for _, server := range page.Data {
			disabled[server.Name] = map[string]bool{"enabled": false}
		}
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == cursor || len(disabled) > 256 {
			return fail(errors.New("MCP inventory exceeds supported limits"))
		}
		cursor = *page.NextCursor
	}
	c.threadConfig = map[string]any{"mcp_servers": disabled}
	return c, nil
}
func (c *Client) Close() {
	c.in.Close()
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	c.cmd.Wait()
	os.RemoveAll(c.dir)
}
func (c *Client) send(v any) error { return json.NewEncoder(c.in).Encode(v) }
func (c *Client) read() (Message, error) {
	for c.out.Scan() {
		var m Message
		if json.Unmarshal(c.out.Bytes(), &m) != nil {
			return m, errors.New("malformed Codex protocol frame")
		}
		if len(m.ID) > 0 && m.Method != "" {
			// Fail closed on every unsolicited server request, including approvals and token refresh.
			if err := c.send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "Unsupported in local Bugsnitch runtime"}}); err != nil {
				return m, err
			}
			continue
		}
		return m, nil
	}
	return Message{}, errors.New("Codex transport closed or frame exceeded limit")
}
func (c *Client) call(method string, params any, dst any) error {
	c.next++
	id := c.next
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		m, err := c.read()
		if err != nil {
			return err
		}
		if string(m.ID) != strconv.Itoa(id) {
			continue
		}
		if len(m.Error) > 0 {
			return fmt.Errorf("Codex rejected %s", method)
		}
		if dst != nil {
			if json.Unmarshal(m.Result, dst) != nil {
				return fmt.Errorf("invalid %s response", method)
			}
		}
		return nil
	}
}
func (c *Client) Models() ([]Model, error) {
	var models []Model
	args := map[string]any{"limit": 100}
	for page := 0; page < 10; page++ {
		var r struct {
			Data []Model `json:"data"`
			Next *string `json:"nextCursor"`
		}
		if err := c.call("model/list", args, &r); err != nil {
			return nil, err
		}
		models = append(models, r.Data...)
		if r.Next == nil {
			return models, nil
		}
		args["cursor"] = *r.Next
	}
	return nil, errors.New("model listing exceeded page limit")
}

func classify(info json.RawMessage) error {
	var kind string
	if json.Unmarshal(info, &kind) == nil {
		switch kind {
		case "unauthorized":
			return SignIn
		case "usageLimitExceeded", "sessionBudgetExceeded":
			return Allowance
		case "rateLimitExceeded":
			return RateLimit
		case "modelNotFound":
			return UnavailableModel
		case "internalServerError", "serverOverloaded", "flexUnavailable", "policyCapacityUnavailable":
			return Provider
		default:
			return Failure("model task failed")
		}
	}
	var details map[string]struct {
		HTTPStatusCode int `json:"httpStatusCode"`
	}
	if json.Unmarshal(info, &details) == nil {
		for _, v := range details {
			switch v.HTTPStatusCode {
			case 401:
				return SignIn
			case 429:
				return RateLimit
			default:
				return Provider
			}
		}
	}
	return Failure("model task failed")
}

func (c *Client) Turn(instructions, prompt, model, effort string, schema any) (Result, error) {
	var r Result
	models, err := c.Models()
	if err != nil {
		return r, err
	}
	var choice *Model
	for i := range models {
		m := &models[i]
		if (model == "" && m.IsDefault) || (model != "" && (model == m.ID || model == m.Model)) {
			choice = m
			break
		}
	}
	if choice == nil {
		return r, UnavailableModel
	}
	if effort != "" {
		found := false
		for _, v := range choice.SupportedReasoningEfforts {
			if v.ReasoningEffort == effort {
				found = true
			}
		}
		if !found {
			return r, errors.New("unsupported reasoning setting for model")
		}
	}
	var thread struct {
		Thread                                    struct{ ID string }
		Cwd, Model, ModelProvider, ApprovalPolicy string
		Sandbox                                   struct {
			Type          string
			NetworkAccess bool
		}
	}
	err = c.call("thread/start", map[string]any{"model": choice.Model, "modelProvider": "openai", "cwd": c.dir, "approvalPolicy": "never", "sandbox": "read-only", "ephemeral": true, "developerInstructions": instructions, "config": c.threadConfig}, &thread)
	if err != nil {
		return r, err
	}
	if thread.Thread.ID == "" || thread.ModelProvider != "openai" || thread.Model != choice.Model || thread.Cwd != c.dir || thread.Sandbox.Type != "readOnly" || thread.Sandbox.NetworkAccess || thread.ApprovalPolicy != "never" {
		return r, errors.New("runtime failed to confirm requested model and read-only permissions")
	}
	var active struct {
		Data []struct {
			RuntimeStatus string                     `json:"runtimeStatus"`
			Tools         map[string]json.RawMessage `json:"tools"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if err = c.call("mcpServerStatus/list", map[string]any{"threadId": thread.Thread.ID, "limit": 256}, &active); err != nil {
		return r, err
	}
	if active.NextCursor != nil {
		return r, errors.New("thread MCP inventory exceeds supported limits")
	}
	for _, server := range active.Data {
		if server.RuntimeStatus != "disabled" || len(server.Tools) != 0 {
			return r, errors.New("runtime has active MCP capabilities; use a dedicated local Codex profile")
		}
	}
	r.Thread = thread.Thread.ID
	r.Model = thread.Model
	args := map[string]any{"threadId": r.Thread, "input": []any{map[string]any{"type": "text", "text": prompt}}}
	if schema != nil {
		args["outputSchema"] = schema
	}
	if effort != "" {
		args["effort"] = effort
	}
	var started struct{ Turn struct{ ID string } }
	if err = c.call("turn/start", args, &started); err != nil {
		return r, err
	}
	r.Turn = started.Turn.ID
	if r.Turn == "" {
		return r, errors.New("missing turn identity")
	}
	for {
		m, err := c.read()
		if err != nil {
			// A disconnected stream can end between cumulative usage updates.
			r.Usage = nil
			return r, err
		}
		switch m.Method {
		case "thread/tokenUsage/updated":
			var n struct {
				ThreadID, TurnID string
				TokenUsage       struct {
					Total struct {
						Input  *int64 `json:"inputTokens"`
						Output *int64 `json:"outputTokens"`
					}
				}
			}
			if json.Unmarshal(m.Params, &n) == nil && n.ThreadID == r.Thread && n.TurnID == r.Turn {
				u := n.TokenUsage.Total
				// Each call creates a fresh ephemeral thread with exactly one turn.
				if u.Input != nil && u.Output != nil && *u.Input >= 0 && *u.Input <= 1<<32 && *u.Output >= 0 && *u.Output <= 1<<32 {
					r.Usage = &ModelUsage{InputTokens: *u.Input, OutputTokens: *u.Output}
				}
			}
		case "item/completed":
			var n struct {
				ThreadID, TurnID string
				Item             struct{ Type, Text, Phase string }
			}
			if json.Unmarshal(m.Params, &n) != nil || n.ThreadID != r.Thread || n.TurnID != r.Turn {
				continue
			}
			if n.Item.Type == "agentMessage" && (n.Item.Phase == "" || n.Item.Phase == "final_answer") {
				if len(n.Item.Text) > 512<<10 {
					return r, errors.New("model artifact too large")
				}
				r.Text = n.Item.Text
			}
		case "turn/completed":
			var n struct {
				ThreadID string
				Turn     struct {
					ID, Status string
					Error      struct{ CodexErrorInfo json.RawMessage }
				}
			}
			if json.Unmarshal(m.Params, &n) != nil || n.ThreadID != r.Thread || n.Turn.ID != r.Turn {
				continue
			}
			switch n.Turn.Status {
			case "completed":
				if strings.TrimSpace(r.Text) == "" {
					return r, errors.New("completed turn returned no final artifact")
				}
				return r, nil
			case "interrupted":
				return r, Interrupted
			default:
				return r, classify(n.Turn.Error.CodexErrorInfo)
			}
		}
	}
}
