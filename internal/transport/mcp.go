// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// OAuth registration, sign-in and consent belong to the operator's authorization server.
type PluginAuth struct{ LocalScopes, LocalToken, Issuer, Introspection, ClientID, ClientSecret, OwnerSubject string }

func (p PluginAuth) oauth() bool {
	return strings.HasPrefix(p.Issuer, "https://") && strings.HasPrefix(p.Introspection, "https://") && p.OwnerSubject != "" && p.ClientID != "" && p.ClientSecret != ""
}
func (p PluginAuth) grant(ctx context.Context, token, audience string) ([]string, string) {
	if len(p.LocalToken) >= 32 && subtle.ConstantTimeCompare([]byte(hash(token)), []byte(hash(p.LocalToken))) == 1 {
		if p.LocalScopes != "" {
			return strings.Fields(p.LocalScopes), "local-grant"
		}
		return []string{"workspace.read", "incident.write", "decision.write"}, "local-grant"
	}
	if !p.oauth() || len(token) < 1 || len(token) > 8192 {
		return nil, ""
	}
	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, "POST", p.Introspection, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, ""
	}
	req.SetBasicAuth(p.ClientID, p.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cl := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, ""
	}
	var t struct {
		Active          bool
		Sub, Iss, Scope string
		Exp             int64
		Aud             json.RawMessage
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&t) != nil || !t.Active || t.Iss != p.Issuer || t.Sub != p.OwnerSubject || t.Exp <= time.Now().Unix() {
		return nil, ""
	}
	var list []string
	if json.Unmarshal(t.Aud, &list) != nil {
		var one string
		if json.Unmarshal(t.Aud, &one) != nil {
			return nil, ""
		}
		list = []string{one}
	}
	if !slices.Contains(list, audience) {
		return nil, ""
	}
	return strings.Fields(t.Scope), "oauth-owner-grant"
}

var pluginIdentity = mcp.Implementation{Name: "bugsnitch", Version: "0.1.0"}

func (s *Server) mountMCP(mux *http.ServeMux) {
	resource := s.Origin + "/mcp"
	discovery := "/.well-known/oauth-protected-resource/mcp"
	mux.HandleFunc("GET "+discovery, func(w http.ResponseWriter, r *http.Request) {
		if !s.Plugin.oauth() {
			respondError(w, problem{503, "Configure the Bugsnitch OAuth authorization server"})
			return
		}
		_ = send(w, 200, object{"resource": resource, "authorization_servers": []string{s.Plugin.Issuer}, "scopes_supported": []string{"workspace.read", "incident.write", "decision.write", "plan.write", "feedback.write", "attempt.control"}})
	})
	type grantKey struct{}
	type grant struct {
		scopes []string
		id     string
	}
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		g := r.Context().Value(grantKey{}).(grant)
		return s.pluginTools("personal", g.scopes, g.id)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 65536})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var g grant
		if ok {
			g.scopes, g.id = s.Plugin.grant(r.Context(), token, resource)
		}
		if !slices.Contains(g.scopes, "workspace.read") {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.Origin+discovery+`", scope="workspace.read"`)
			respondError(w, problem{401, "A scoped Bugsnitch plugin grant is required"})
			return
		}
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), grantKey{}, g)))
	})
}

type reportArgs struct {
	Project   string `json:"projectId"`
	Observed  string `json:"observed"`
	Expected  string `json:"expected"`
	Steps     string `json:"steps"`
	RequestID string `json:"requestId"`
}
type answerArgs struct {
	DecisionID string `json:"decisionId"`
	Answer     string `json:"answer"`
	Version    int64  `json:"version"`
	RequestID  string `json:"requestId"`
}

func (s *Server) pluginTools(ws string, scopes []string, grantID string) *mcp.Server {
	server := mcp.NewServer(&pluginIdentity, nil)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(audit.WithActor(ctx, audit.Plugin, grantID), method, req)
		}
	})
	for _, item := range []struct{ name, view, description string }{{"list_projects", "projects", "List projects owned by this workspace and their current dispatch settings."}, {"list_decisions", "decisions", "Inspect decision versions, options and current status before asking the owner for a choice."}, {"list_attempts", "attempts", "Inspect the latest machine-reported execution state, revision and last update."}} {
		mcp.AddTool(server, &mcp.Tool{Name: item.name, Description: item.description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			var v json.RawMessage
			err := s.DB.QueryRowContext(ctx, "SELECT COALESCE(json_agg(t),'[]') FROM (SELECT * FROM ("+views[item.view]+") v LIMIT 100)t", ws).Scan(&v)
			if err != nil {
				return nil, nil, errors.New("Workspace temporarily unavailable")
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(v)}}}, nil, nil
		})
	}
	if slices.Contains(scopes, "incident.write") {
		mcp.AddTool(server, &mcp.Tool{Name: "report_incident", Description: "Record an owner-authorized manual report in a selected project. Reuse the same requestId on retry. This records triage evidence."}, func(ctx context.Context, _ *mcp.CallToolRequest, a reportArgs) (*mcp.CallToolResult, any, error) {
			id, err := s.Incidents.Report(ctx, ws, a.Project, a.Observed, a.Expected, a.Steps, a.RequestID)
			if err != nil {
				return nil, nil, errors.New("Report rejected. Check project, required fields and request ID.")
			}
			return nil, object{"id": id}, nil
		})
	}
	if slices.Contains(scopes, "decision.write") {
		mcp.AddTool(server, &mcp.Tool{Name: "answer_decision", Description: "Apply the owner's explicit answer to a pending decision at the reviewed version. Reuse the request ID after a lost response."}, func(ctx context.Context, _ *mcp.CallToolRequest, a answerArgs) (*mcp.CallToolResult, any, error) {
			err := s.applyAnswer(ctx, ws, a.DecisionID, decisionAnswer{Answer: a.Answer, Version: a.Version, RequestID: a.RequestID})
			if err != nil {
				var p problem
				if errors.As(err, &p) {
					return nil, nil, p
				}
				return nil, nil, errors.New("Decision unavailable")
			}
			return nil, object{"id": a.DecisionID, "answer": a.Answer}, nil
		})
	}
	s.addProjectTools(server, ws, scopes)
	return server
}
