// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/journalbridge"
)

// A short Git operation may already have changed shared Git metadata when the
// caller cancels. Let the native command finish and report its actual result.
// The next operation checks the context again. Each native Git call is timed.
func nativeOperation(ctx context.Context, mode string, request, response any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if mode != "--worktree" && mode != "--integration" {
		return errors.New("unsupported native runner operation")
	}
	path, err := journalbridge.HelperPath()
	if err != nil {
		return err
	}
	env, err := nativeGitEnv()
	if err != nil {
		return err
	}
	body, err := json.Marshal(request)
	if err != nil || len(body) > 256<<10 {
		return errors.New("invalid native runner request")
	}
	cmd := exec.Command(path, mode)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(body)
	output := capped{max: 256 << 10}
	cmd.Stdout = &output
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Run(); err != nil || output.overflow {
		return errors.New("native runner operation interrupted or incompatible; inspect the retained worktree and matching runner bundle")
	}
	var reply struct {
		Version int             `json:"version"`
		Value   json.RawMessage `json:"value"`
		Error   string          `json:"error"`
	}
	if err = json.Unmarshal(output.buffer.Bytes(), &reply); err != nil || reply.Version != 1 {
		return errors.New("incompatible native runner response")
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	if len(reply.Value) == 0 {
		return errors.New("missing native runner result")
	}
	if response != nil {
		return json.Unmarshal(reply.Value, response)
	}
	return nil
}

func nativeGitEnv() ([]string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil || !filepath.IsAbs(gitPath) {
		return nil, errors.New("Git must be installed on an absolute PATH")
	}
	var paths []string
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(entry) {
			paths = append(paths, entry)
		}
	}
	return []string{"PATH=" + strings.Join(paths, string(os.PathListSeparator)), "BUGSNITCH_GIT=" + gitPath}, nil
}
