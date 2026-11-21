// SPDX-License-Identifier: MPL-2.0
// Command bugsnitch-mcp exposes authorized Bugsnitch tools to a local STDIO host.
package main

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	endpoint := os.Getenv("BUGSNITCH_ORIGIN")
	if endpoint == "" {
		endpoint = "http://localhost:8787"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !(u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		return fmt.Errorf("invalid service origin")
	}
	tok := strings.TrimSpace(os.Getenv("BUGSNITCH_MCP_TOKEN"))
	if tok == "" {
		return fmt.Errorf("set a scoped BUGSNITCH_MCP_TOKEN")
	}
	cfg := &mcp.Implementation{Name: "bugsnitch-desktop", Version: "0.1.0"}
	web := &http.Client{Transport: bearer{tok}, Timeout: 25 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	ctx := context.Background()
	conn, err := mcp.NewClient(cfg, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint + "/mcp", HTTPClient: web}, nil)
	if err != nil {
		return fmt.Errorf("could not connect to the authorized Bugsnitch service")
	}
	defer conn.Close()
	local := mcp.NewServer(cfg, nil)
	for tool, err := range conn.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("could not load authorized tools")
		}
		local.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return conn.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: req.Params.Arguments})
		})
	}
	return local.Run(ctx, &mcp.StdioTransport{})
}
