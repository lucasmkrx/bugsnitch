// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

// Only active, authenticated subscriptions occupy memory. The workspace
// cursor closes races around notification delivery.
type ProgressionWakes struct {
	mu     sync.Mutex
	active map[string][]chan struct{}
	count  int
}

func (w *ProgressionWakes) Signal(project string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ch := range w.active[project] {
		close(ch)
	}
	delete(w.active, project)
}

func (w *ProgressionWakes) Wait(ctx context.Context, project string) {
	w.mu.Lock()
	if w.count >= 8 {
		w.mu.Unlock()
		return
	}
	if w.active == nil {
		w.active = make(map[string][]chan struct{})
	}
	ch := make(chan struct{})
	w.active[project] = append(w.active[project], ch)
	w.count++
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.count--
		list := w.active[project]
		for i, item := range list {
			if item == ch {
				w.active[project] = append(list[:i], list[i+1:]...)
				if len(w.active[project]) == 0 {
					delete(w.active, project)
				}
				break
			}
		}
	}()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-ch:
	case <-timer.C:
	}
}

func (s *Server) waitProgression(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct{ Project string }
	if json.Unmarshal(raw, &p) != nil || p.Project == "" {
		return nil, bad("Invalid wake scope")
	}
	if s.ProgressionWakes != nil {
		s.ProgressionWakes.Wait(ctx, p.Project)
	}
	return true, nil
}

// Rust derives the monitor subjects under the current device assignment. The
// evaluator then rechecks that the verified deployment still matches that read.
func (s *Server) progressionVerdict(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		Workspace   string
		Project     string
		Deployments []protocol.ActiveRelease
	}
	if err := json.Unmarshal(raw, &input); err != nil || len(input.Deployments) > 32 {
		return nil, bad("Invalid deployment observations")
	}
	for i := range input.Deployments {
		d := &input.Deployments[i]
		v, err := (incidents.Store{DB: s.DB}).Verdict(ctx, input.Workspace, input.Project, d.IncidentID, d.Environment)
		if err != nil {
			return nil, err
		}
		d.Verdict = v.Status
		if v.Deployment == nil || v.Deployment.CommandID != d.DeploymentKey ||
			v.Deployment.MonitorID != d.MonitorID || v.Deployment.Candidate != d.Candidate || v.Deployment.Build != d.Build {
			d.Verdict = "Awaiting evidence for the active deployment"
		}
	}
	return input.Deployments, nil
}
