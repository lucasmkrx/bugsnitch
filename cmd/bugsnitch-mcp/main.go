// SPDX-License-Identifier: MPL-2.0
// Command bugsnitch-mcp exposes authorized Bugsnitch tools to a local STDIO host.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearer struct{ origin, token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != b.origin || r.URL.User != nil {
		return nil, errors.New("refusing to send a credential outside the configured service origin")
	}
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
	if len(os.Args) > 1 {
		if len(os.Args) != 4 || os.Args[1] != "configure" || os.Args[2] != "--origin" {
			return errors.New("usage: bugsnitch-mcp [configure --origin SERVICE_ORIGIN < token-file]")
		}
		input, err := os.Stdin.Stat()
		if err != nil || input.Mode()&os.ModeCharDevice != 0 {
			return errors.New("redirect the scoped plugin token to stdin from a file or pipe")
		}
		dir, err := profileDir()
		if err != nil {
			return err
		}
		if err = saveProfile(dir, os.Args[3], os.Stdin); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Saved the private Bugsnitch MCP connection.")
		return nil
	}
	connection, err := loadConnection(os.Getenv("BUGSNITCH_ORIGIN"), os.Getenv("BUGSNITCH_MCP_TOKEN"))
	if err != nil {
		return err
	}
	cfg := &mcp.Implementation{Name: "bugsnitch-desktop", Version: "0.1.0"}
	web := &http.Client{Transport: bearer{origin: connection.Origin, token: connection.Token}, Timeout: 25 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	ctx := context.Background()
	conn, err := mcp.NewClient(cfg, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: connection.Origin + "/mcp", HTTPClient: web}, nil)
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
