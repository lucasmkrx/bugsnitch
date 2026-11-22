// SPDX-License-Identifier: MPL-2.0
package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
)

// Each slot serves one transaction at a time using a private child and libpq
// session. Separate slots permit competing claims to meet at PostgreSQL locks.
const coordinationWorkers = 4
const maxCoordinationFrame = 4 << 20

type CoordinationError struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

func (e *CoordinationError) Error() string { return e.Message }

// ErrUnknownOutcome means a transaction may have committed before its reply was lost.
var ErrUnknownOutcome = errors.New("coordination connection lost; reconcile the operation before retrying")

type coordinator struct {
	driver.Driver
	path      string
	env       []string
	available chan int
	workers   [coordinationWorkers]coordWorker
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}
type nativeConnector struct {
	driver.Connector
	service *coordinator
}

func (c *nativeConnector) Driver() driver.Driver { return c.service }
func (c *nativeConnector) Close() error {
	c.service.close()
	return nil
}

func newCoordinator(d driver.Driver, path string, env []string) *coordinator {
	ctx, cancel := context.WithCancel(context.Background())
	c := &coordinator{Driver: d, path: path, env: env, available: make(chan int, coordinationWorkers), ctx: ctx, cancel: cancel}
	for i := range c.workers {
		c.available <- i
	}
	return c
}
func (c *coordinator) close() {
	c.closeOnce.Do(func() {
		c.cancel()
		for range c.workers {
			i := <-c.available
			c.workers[i].stop()
		}
	})
}

type coordWorker struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (w *coordWorker) start(path string, env []string) error {
	cmd := exec.Command(path, "--coordinate")
	cmd.Env = env
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return err
	}
	if err = cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return err
	}
	w.cmd, w.in, w.out = cmd, input, output
	return nil
}
func (w *coordWorker) stop() {
	if w.cmd == nil {
		return
	}
	_ = w.in.Close()
	_ = w.cmd.Process.Kill()
	_ = w.out.Close()
	_ = w.cmd.Wait()
	*w = coordWorker{}
}
func (w *coordWorker) exchange(payload []byte) ([]byte, error) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.in.Write(header[:]); err != nil {
		return nil, err
	}
	if _, err := w.in.Write(payload); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(w.out, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxCoordinationFrame {
		return nil, ErrUnknownOutcome
	}
	buf := make([]byte, size)
	_, err := io.ReadFull(w.out, buf)
	return buf, err
}

// Coordinate sends a named, server-constructed operation. A missing reply can
// follow COMMIT. The bridge never retries an uncertain request or logs its body.
func Coordinate(ctx context.Context, db *sql.DB, operation string, input, output any) error {
	c, ok := db.Driver().(*coordinator)
	if !ok {
		return errors.New("database was opened without Bugsnitch coordination")
	}
	source, id := audit.Actor(ctx)
	body, err := json.Marshal(struct {
		Version int       `json:"version"`
		Op      string    `json:"op"`
		Actor   [2]string `json:"actor"`
		Input   any       `json:"input"`
	}{1, operation, [2]string{source, id}, input})
	if err != nil || len(body) > maxCoordinationFrame {
		return errors.New("invalid coordination request")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var index int
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return errors.New("coordination service closed")
	case index = <-c.available:
	}
	defer func() { c.available <- index }()
	if c.ctx.Err() != nil {
		return errors.New("coordination service closed")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	w := &c.workers[index]
	if w.cmd == nil {
		if err = w.start(c.path, c.env); err != nil {
			return errors.New("could not start the hosted coordination helper")
		}
	}
	var data []byte
	var exchangeErr error
	done := make(chan struct{})
	go func() { defer close(done); data, exchangeErr = w.exchange(body) }()
	select {
	case <-done:
	case <-ctx.Done():
		_ = w.cmd.Process.Kill()
		<-done
		w.stop()
		return errors.Join(ctx.Err(), ErrUnknownOutcome)
	case <-c.ctx.Done():
		_ = w.cmd.Process.Kill()
		<-done
		w.stop()
		return ErrUnknownOutcome
	}
	var reply struct {
		Version int                `json:"version"`
		Value   json.RawMessage    `json:"value"`
		Error   *CoordinationError `json:"error"`
	}
	if exchangeErr != nil || json.Unmarshal(data, &reply) != nil || reply.Version != 1 || (reply.Error == nil) == (len(reply.Value) == 0) {
		w.stop()
		return ErrUnknownOutcome
	}
	if reply.Error != nil {
		// Retire a failed database session without replaying its request.
		if reply.Error.Status >= 500 {
			w.stop()
		}
		return reply.Error
	}
	if output != nil && json.Unmarshal(reply.Value, output) != nil {
		return ErrUnknownOutcome
	}
	return nil
}
