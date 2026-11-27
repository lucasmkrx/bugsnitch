// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"strings"
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
<body data-dashboard="{{.Origin}}" data-paired="{{.Paired}}">
<main><h1>Bugsnitch runner connection</h1>
<p id="status">Connecting to Bugsnitch Settings…</p></main>
<script src="/bridge.js"></script>
</body>
</html>
`))

type modelSettingsReply struct {
	Model  string   `json:"model,omitempty"`
	Models []string `json:"models,omitempty"`
	Error  string   `json:"error,omitempty"`
}

type ModelSettingsGrant struct {
	DeviceID string `json:"deviceId"`
	Expiry   int64  `json:"expiry"`
	Proof    string `json:"proof"`
}

type modelSettingsAuthorization func(context.Context, string, *ModelSettingsGrant) (ModelSettingsGrant, error)

func modelSettingsOrigin(dashboardOrigin string) (string, error) {
	dashboard, err := url.Parse(dashboardOrigin)
	if err != nil || dashboard.Hostname() == "" || dashboard.User != nil ||
		(dashboard.Path != "" && dashboard.Path != "/") || dashboard.RawQuery != "" ||
		dashboard.ForceQuery || dashboard.Fragment != "" {
		return "", errors.New("Use the dashboard origin, such as https://bugsnitch.example.com")
	}
	ip := net.ParseIP(dashboard.Hostname())
	if dashboard.Scheme != "https" && !(dashboard.Scheme == "http" && (dashboard.Hostname() == "localhost" || ip.IsLoopback())) {
		return "", errors.New("The dashboard origin must use HTTPS or local HTTP")
	}
	return dashboard.Scheme + "://" + dashboard.Host, nil
}

func modelSettingsToken() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(secret[:]), nil
}

func modelSettingsDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ServeModelSettings exposes a short-lived browser handoff on the runner's
// loopback interface. The URL fragment never appears in HTTP requests.
func ServeModelSettings(ctx context.Context, dashboardOrigin string, output io.Writer) error {
	origin, err := modelSettingsOrigin(dashboardOrigin)
	if err != nil {
		return err
	}

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
	token, err := modelSettingsToken()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	host := listener.Addr().String()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	fmt.Fprintf(output, "In %s, open Settings → API keys and paste this runner connection URL:\nhttp://%s/#%s\nKeep this terminal open while making changes. Press Ctrl-C when finished.\n", origin, host, token)
	return serveModelSettings(ctx, listener, modelSettingsHandler(host, origin, token))
}

// StartPairedModelSettings serves credential settings on loopback while the
// paired device is syncing. A local browser and the workspace owner authorize
// each short-lived connection. No API key is sent to the workspace service.
func StartPairedModelSettings(ctx context.Context, dashboardOrigin string, authorize modelSettingsAuthorization) error {
	origin, err := modelSettingsOrigin(dashboardOrigin)
	if err != nil {
		return err
	}
	dir, err := home()
	if err != nil {
		return err
	}
	if err := checkModelSettingsDir(dir); err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:43182")
	if err != nil {
		return fmt.Errorf("start local API key settings: %w", err)
	}
	go func() {
		defer listener.Close()
		err := serveModelSettings(ctx, listener, modelSettingsRoutes(listener.Addr().String(), origin, "", authorize))
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "Local API key settings stopped.")
		}
	}()
	return nil
}

func serveModelSettings(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      25 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    8 << 10,
		Handler:           handler,
	}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	if err := server.Serve(listener); errors.Is(err, http.ErrServerClosed) {
		return nil
	} else {
		return err
	}
}

func modelSettingsHandler(host, dashboardOrigin, token string) http.HandlerFunc {
	return modelSettingsRoutes(host, dashboardOrigin, token, nil)
}

func modelSettingsRoutes(host, dashboardOrigin, token string, authorize modelSettingsAuthorization) http.HandlerFunc {
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
			_ = modelSettingsPage.Execute(w, struct {
				Origin string
				Paired bool
			}{dashboardOrigin, authorize != nil})
		case r.Method == http.MethodGet && r.URL.Path == "/bridge.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(modelSettingsBridge)
		case r.Method == http.MethodPost && r.URL.Path == "/connect" && authorize != nil:
			if r.Header.Get("Origin") != "http://"+host || r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "Invalid runner connection", http.StatusForbidden)
				return
			}
			session, err := modelSettingsToken()
			if err != nil {
				http.Error(w, "Could not open the runner connection", http.StatusServiceUnavailable)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			grant, err := authorize(ctx, modelSettingsDigest(session), nil)
			if err != nil {
				http.Error(w, "Could not verify this paired runner", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(struct {
				Token string             `json:"token"`
				Claim ModelSettingsGrant `json:"claim"`
			}{session, grant})
		case r.Method == http.MethodPost && r.URL.Path == "/settings":
			bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if r.Header.Get("Origin") != "http://"+host || !ok ||
				len(bearer) != 64 || (authorize == nil && subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) != 1) {
				http.Error(w, "Invalid runner connection", http.StatusForbidden)
				return
			}
			if r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "Use application/json", http.StatusUnsupportedMediaType)
				return
			}
			var input struct {
				Action, APIKey, Model string
				Permit                *ModelSettingsGrant
			}
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			dec.DisallowUnknownFields()
			if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
				http.Error(w, "Invalid model settings request", http.StatusBadRequest)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			if authorize != nil {
				if input.Permit == nil {
					http.Error(w, "Missing workspace owner authorization", http.StatusForbidden)
					return
				}
				approval, cancel := context.WithTimeout(ctx, 8*time.Second)
				_, err := authorize(approval, modelSettingsDigest(bearer), input.Permit)
				cancel()
				if err != nil {
					http.Error(w, "Runner authorization expired", http.StatusForbidden)
					return
				}
			}
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
