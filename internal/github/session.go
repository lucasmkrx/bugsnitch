// SPDX-License-Identifier: MPL-2.0
package github

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

type fields map[string]any

// Each domain transaction owns this child until its last provider operation.
// JWTs, installation tokens and signing key contents never cross the pipe.
type Repository struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
	mu     sync.Mutex
	closed bool
}

func (a *App) open(ctx context.Context, install, repo int64, name, scope string) (*Repository, error) {
	var args []string
	if a.fixture != "" {
		args = []string{"--loopback-fixture", a.fixture}
	}
	cmd := a.command(ctx, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return nil, errors.New("GitHub provider unavailable")
	}
	r := &Repository{cmd: cmd, in: in, out: out}
	if err = r.call(ctx, fields{"op": "open", "installation": install, "repo": repo, "name": name, "scope": scope}, nil); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}
func (r *Repository) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.in.Close()
	_ = r.cmd.Process.Kill()
	r.out.Close()
	_ = r.cmd.Wait()
}

func (r *Repository) call(ctx context.Context, request fields, output any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("GitHub provider session closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := json.Marshal(fields{"version": 1, "request": request})
	if err != nil || len(body) > 256<<10 {
		return errors.New("invalid GitHub provider request")
	}
	// Bound the whole operation as well as individual HTTPS calls. A lost
	// response to a write is returned to the durable outbox for reconciliation.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = r.cmd.Process.Kill() })
	defer stop()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	lost := errors.New("GitHub provider response unavailable")
	if _, err = r.in.Write(header[:]); err != nil {
		return lost
	}
	if _, err = r.in.Write(body); err != nil {
		return lost
	}
	if _, err = io.ReadFull(r.out, header[:]); err != nil {
		return lost
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > 1<<20 {
		return lost
	}
	raw := make([]byte, n)
	if _, err = io.ReadFull(r.out, raw); err != nil {
		return lost
	}
	var reply struct {
		Version int
		Value   json.RawMessage
		Error   *struct {
			Kind   string
			Status int
			Retry  int64 `json:"retry_seconds"`
		}
	}
	if json.Unmarshal(raw, &reply) != nil || reply.Version != 1 || (reply.Error == nil) == (len(reply.Value) == 0) {
		return lost
	}
	if e := reply.Error; e != nil {
		switch e.Kind {
		case "subject_changed":
			return ErrSubjectChanged
		case "http":
			if e.Status >= 100 && e.Status <= 599 && e.Retry >= 0 && e.Retry <= 86401 {
				return &APIError{e.Status, time.Duration(e.Retry) * time.Second}
			}
		case "invalid":
			return errors.New("invalid GitHub provider operation")
		case "configuration":
			return errors.New("invalid GitHub App configuration")
		case "connection":
			return errors.New("GitHub connection unavailable")
		}
		return errors.New("unexpected GitHub response")
	}
	if output != nil && json.Unmarshal(reply.Value, output) != nil {
		return lost
	}
	return nil
}
