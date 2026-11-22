// SPDX-License-Identifier: MPL-2.0
// Package journalbridge accesses the local runner's two SQLite journals through
// its private native helper. The caller supplies SQL assembled by trusted code.
package journalbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const maxFrame = 64 << 20

// HelperPath resolves the package's companion executable without searching the
// current directory or PATH. Tests and development builds can supply its full path.
func HelperPath() (string, error) {
	if override := os.Getenv("BUGSNITCH_JOURNAL_HELPER"); override != "" {
		if !filepath.IsAbs(override) {
			return "", errors.New("BUGSNITCH_JOURNAL_HELPER must be an absolute path")
		}
		return override, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "bugsnitch-runner-journal"
	switch filepath.Base(exe) {
	case "bugsnitch-darwin-arm64":
		name += "-darwin-arm64"
	case "bugsnitch-darwin-amd64":
		name += "-darwin-amd64"
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(exe), name), nil
}

type client struct {
	gate chan struct{}
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
}

var shared = client{gate: make(chan struct{}, 1)}

// Query executes one batch. A transport failure can follow a committed write.
// The caller must reconcile against the journal before retrying that operation.
func Query(ctx context.Context, file, sql string) ([]byte, error) {
	return shared.query(ctx, file, sql)
}

func (c *client) query(ctx context.Context, file, sql string) ([]byte, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	if name := filepath.Base(path); name != "runner.sqlite" && name != "device.sqlite" {
		return nil, errors.New("unsupported journal file")
	}
	frame, err := json.Marshal(struct {
		Version int    `json:"version"`
		Path    string `json:"path"`
		SQL     string `json:"sql"`
	}{1, path, sql})
	if err != nil {
		return nil, err
	}
	if len(frame) > maxFrame {
		return nil, errors.New("journal batch exceeds the frame limit")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if c.cmd == nil {
		if err := c.start(); err != nil {
			return nil, err
		}
	}
	var data []byte
	var databaseErr, transportErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		data, databaseErr, transportErr = c.exchange(frame)
	}()
	select {
	case <-done:
		if transportErr != nil {
			c.stop()
			return nil, fmt.Errorf("journal helper connection lost; operation outcome unknown: %w", transportErr)
		}
		return data, databaseErr
	case <-ctx.Done():
		// Only this caller owns the pipe. Killing the child unblocks both directions.
		_ = c.cmd.Process.Kill()
		<-done
		c.stop()
		return nil, fmt.Errorf("journal interrupted; operation outcome unknown: %w", ctx.Err())
	}
}

func (c *client) start() error {
	path, err := HelperPath()
	if err != nil {
		return err
	}
	cmd := exec.Command(path, "--stdio")
	cmd.Env = []string{}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return fmt.Errorf("start Bugsnitch journal helper: %w", err)
	}
	c.cmd, c.in, c.out = cmd, in, out
	return nil
}

// stop is called under the gate after the exchange has returned.
func (c *client) stop() {
	if c.cmd == nil {
		return
	}
	_ = c.in.Close()
	_ = c.cmd.Process.Kill()
	_ = c.out.Close()
	_ = c.cmd.Wait()
	c.cmd = nil
}

func (c *client) exchange(frame []byte) (data []byte, databaseErr, transportErr error) {
	var header [5]byte
	binary.BigEndian.PutUint32(header[1:], uint32(len(frame)))
	if _, err := c.in.Write(header[1:]); err != nil {
		return nil, nil, err
	}
	if _, err := c.in.Write(frame); err != nil {
		return nil, nil, err
	}
	if _, err := io.ReadFull(c.out, header[:]); err != nil {
		return nil, nil, err
	}
	size := binary.BigEndian.Uint32(header[1:])
	if size > maxFrame || (header[0] != 0 && size != 0) {
		return nil, nil, errors.New("invalid journal helper reply")
	}
	switch header[0] {
	case 0:
		data = make([]byte, size)
		_, err := io.ReadFull(c.out, data)
		return data, nil, err
	case 1:
		return nil, errors.New("journal helper protocol incompatible"), nil
	case 2:
		return nil, errors.New("SQLite journal operation failed"), nil
	case 3:
		return nil, errors.New("SQLite journal busy or locked"), nil
	case 4:
		return nil, errors.New("SQLite journal constraint failed"), nil
	case 6:
		return nil, errors.New("runner journal requires a compatible Bugsnitch version"), nil
	case 5:
		return nil, errors.New("journal result exceeds the frame limit"), nil
	default:
		return nil, nil, errors.New("unrecognized journal helper status")
	}
}
