// SPDX-License-Identifier: MPL-2.0
// Package testserver starts a packaged server for public contract tests.
package testserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type Config struct{ Origin, OwnerToken, PluginToken, WebDir, GitHubSecret, SentrySecret, SlackSecret, TelegramSecret, LinearSecret string }

// Start forwards requests to the packaged server over loopback. Tests retain
// their own database connections to exercise precise failure conditions.
func Start(t *testing.T, dsn string, cfg Config) http.Handler {
	t.Helper()
	if cfg.Origin == "" {
		cfg.Origin = "http://localhost:8787"
	}
	if cfg.OwnerToken == "" {
		cfg.OwnerToken = "isolated-test-configuration-only-owner"
	}
	binary := os.Getenv("BUGSNITCH_TEST_SERVER")
	if binary == "" {
		binary = filepath.Join(filepath.Dir(os.Getenv("BUGSNITCH_MIGRATOR")), "bugsnitch-server")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("set BUGSNITCH_TEST_SERVER to the packaged Rust server or run make test")
	}
	cmd := exec.Command(binary)
	cmd.Env = []string{"DATABASE_URL=" + dsn, "BUGSNITCH_LISTEN=127.0.0.1:0", "BUGSNITCH_ORIGIN=" + cfg.Origin,
		"BUGSNITCH_OWNER_TOKEN=" + cfg.OwnerToken, "BUGSNITCH_MCP_TOKEN=" + cfg.PluginToken, "BUGSNITCH_WEB_DIR=" + cfg.WebDir, "SENTRY_CLIENT_SECRET=" + cfg.SentrySecret, "GITHUB_WEBHOOK_SECRET=" + cfg.GitHubSecret, "SLACK_SIGNING_SECRET=" + cfg.SlackSecret, "TELEGRAM_WEBHOOK_SECRET=" + cfg.TelegramSecret, "LINEAR_WEBHOOK_SECRET=" + cfg.LinearSecret}
	for _, key := range []string{"HOME", "USER", "TMPDIR", "PATH", "KRB5CCNAME", "KRB5_CONFIG", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	// The kernel allocates the port. Only the installed server's startup
	// record supplies its address, so concurrent tests cannot steal a port.
	go func() {
		defer close(ready)
		scanner := bufio.NewScanner(stderr)
		announced := false
		for scanner.Scan() {
			var msg struct {
				Message string
				Address string
			}
			if json.Unmarshal(scanner.Bytes(), &msg) == nil && msg.Message == "listening" && !announced {
				announced = true
				ready <- msg.Address
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		finished := make(chan error, 1)
		go func() { finished <- cmd.Wait() }()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("Rust test server exited: %v", err)
			}
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-finished
			t.Error("Rust test server did not shut down")
		}
	})
	var addr string
	select {
	case addr = <-ready:
	case <-time.After(35 * time.Second):
		t.Fatal("Rust test server startup timed out")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "" {
		t.Fatalf("Rust test server did not announce its loopback listener: %q", addr)
	}
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// Some tests replace the global transport to fixture the runner's own
	// GitHub reads. Use a dedicated transport for the live Bugsnitch socket.
	client := &http.Transport{DisableCompression: true, ResponseHeaderTimeout: 12 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	proxy.Transport = client
	origin, err := url.Parse(cfg.Origin)
	if err != nil {
		t.Fatal(err)
	}
	direct := proxy.Director
	proxy.Director = func(r *http.Request) { direct(r); r.Host = origin.Host }
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		t.Errorf("Rust test listener: %v", err)
		http.Error(w, fmt.Sprint(http.StatusBadGateway), http.StatusBadGateway)
	}
	return proxy
}
