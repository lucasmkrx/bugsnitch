// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
)

//go:embed model_settings_bridge.js
var modelSettingsBridge []byte

var modelSettingsPage = template.Must(template.New("model settings").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bugsnitch runner connection</title>
<body data-dashboard="{{.}}">
<main><h1>Bugsnitch runner connection</h1>
<p id="status">Return to Bugsnitch Settings to enter your OpenAI key and choose a model.</p></main>
<script src="/bridge.js"></script>
</body>
</html>
`))

type modelSettingsReply struct {
	Model  string   `json:"model,omitempty"`
	Models []string `json:"models,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// ServeModelSettings exposes a short-lived browser handoff on the runner's
// loopback interface. The URL fragment never appears in HTTP requests.
func ServeModelSettings(ctx context.Context, dashboardOrigin string, output io.Writer) error {
	dashboard, err := url.Parse(dashboardOrigin)
	if err != nil || dashboard.Hostname() == "" || dashboard.User != nil ||
		(dashboard.Path != "" && dashboard.Path != "/") || dashboard.RawQuery != "" ||
		dashboard.ForceQuery || dashboard.Fragment != "" {
		return errors.New("Use the dashboard origin, such as https://bugsnitch.example.com")
	}
	ip := net.ParseIP(dashboard.Hostname())
	if dashboard.Scheme != "https" && !(dashboard.Scheme == "http" && (dashboard.Hostname() == "localhost" || ip.IsLoopback())) {
		return errors.New("The dashboard origin must use HTTPS or local HTTP")
	}
	dashboardOrigin = dashboard.Scheme + "://" + dashboard.Host

	dir, err := home()
	if err != nil {
		return err
	}
	if err := checkModelSettingsDir(dir); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "model-settings.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		return errors.New("A model settings session is already open for this runner")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(secret[:])
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	host := listener.Addr().String()
	localOrigin := "http://" + host
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    8 << 10,
		Handler:           modelSettingsHandler(host, dashboardOrigin, token),
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	fmt.Fprintf(output, "In %s, open Settings → Model access and paste this runner connection URL:\n%s/#%s\nKeep this terminal open while making changes. Press Ctrl-C when finished.\n", dashboardOrigin, localOrigin, token)
	if err := server.Serve(listener); errors.Is(err, http.ErrServerClosed) {
		return nil
	} else {
		return err
	}
}

func modelSettingsHandler(host, dashboardOrigin, token string) http.HandlerFunc {
	var changes sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != host {
			http.Error(w, "Invalid host", http.StatusForbidden)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = modelSettingsPage.Execute(w, dashboardOrigin)
		case r.Method == http.MethodGet && r.URL.Path == "/bridge.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(modelSettingsBridge)
		case r.Method == http.MethodPost && r.URL.Path == "/settings":
			if r.Header.Get("Origin") != "http://"+host ||
				subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				http.Error(w, "Invalid runner connection", http.StatusForbidden)
				return
			}
			if r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "Use application/json", http.StatusUnsupportedMediaType)
				return
			}
			var input struct{ Action, APIKey, Model string }
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			dec.DisallowUnknownFields()
			if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
				http.Error(w, "Invalid model settings request", http.StatusBadRequest)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			if input.Action == "save" || input.Action == "remove" {
				changes.Lock()
				defer changes.Unlock()
			}
			reply := changeModelSettings(ctx, input.Action, input.APIKey, input.Model)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(reply)
		default:
			http.NotFound(w, r)
		}
	}
}

func changeModelSettings(ctx context.Context, action, key, model string) modelSettingsReply {
	if action == "remove" {
		if err := removeOpenAISettings(); err != nil {
			return modelSettingsReply{Error: "Could not remove the local OpenAI settings."}
		}
		return modelSettingsReply{}
	}
	if action == "status" || key == "" {
		saved, err := loadOpenAISettings()
		if err != nil {
			return modelSettingsReply{Error: "Could not read the local OpenAI settings. Enter your key again to replace them."}
		}
		if action == "status" {
			return modelSettingsReply{Model: saved.Model}
		}
		key = saved.Key
	}
	if !agent.ValidOpenAIKey(key) {
		return modelSettingsReply{Error: "Enter a valid OpenAI API key."}
	}
	switch action {
	case "models":
		models, err := agent.OpenAIModels(ctx, key)
		if err != nil {
			if errors.Is(err, agent.OpenAIKeyRequired) {
				return modelSettingsReply{Error: "OpenAI rejected the key or it lacks Models read permission. Check the key or enter a model ID manually."}
			}
			return modelSettingsReply{Error: "Could not load models from OpenAI. Try again or enter a model ID manually."}
		}
		return modelSettingsReply{Models: models}
	case "save":
		if !agent.ValidOpenAIModel(model) {
			return modelSettingsReply{Error: "Enter a valid OpenAI model ID."}
		}
		if err := saveOpenAISettings(openAISettings{Key: key, Model: model}); err != nil {
			return modelSettingsReply{Error: "Could not save the OpenAI settings. Check the runner's local settings directory."}
		}
		return modelSettingsReply{Model: model}
	default:
		return modelSettingsReply{Error: "Unknown model settings action."}
	}
}
