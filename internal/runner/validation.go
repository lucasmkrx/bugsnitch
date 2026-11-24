// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/lucasmkrx/bugsnitch/internal/journalbridge"
)

var errValidationUnconfirmed = errors.New("validator termination unconfirmed; inspect the retained worktree and local processes before recovery")

type validationLaunch struct {
	Sandbox        string               `json:"sandbox,omitempty"`
	Version        int                  `json:"version"`
	Worktree       string               `json:"worktree"`
	Project        string               `json:"project"`
	Argv           []string             `json:"argv"`
	TimeoutSeconds int                  `json:"timeout_seconds"`
	Home           string               `json:"home"`
	Repository     string               `json:"repository"`
	Attempt        *worktreeIdentity    `json:"attempt,omitempty"`
	CandidateSHA   string               `json:"candidate_sha,omitempty"`
	Integration    *integrationIdentity `json:"integration,omitempty"`
}

func validate(ctx context.Context, a *Attempt) (*int, error) {
	return validateCombined(ctx, a, nil)
}

// A nil check selects the attempt worktree. An integration check binds the
// disposable detached worktree to its recorded target and combined commit.
func validateCombined(ctx context.Context, a *Attempt, check *IntegrationCheck) (*int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	secs := a.Project.Config.ValidationTimeoutSeconds
	if secs == 0 {
		secs = 600
	}
	attempt, err := a.nativeWorktreeIdentity()
	if err != nil {
		return nil, err
	}
	params := validationLaunch{Version: 1, Sandbox: a.Project.Config.ValidationSandbox, Worktree: a.Worktree, Project: attempt.Project,
		Argv: a.Project.Config.Validate, TimeoutSeconds: secs, Home: attempt.Home, Repository: attempt.Repository}
	if check == nil {
		params.Attempt, params.CandidateSHA = &attempt, a.CandidateSHA
	} else {
		receipt := check.identity()
		params.Integration, params.Worktree = &receipt, check.Worktree
	}
	frame, err := json.Marshal(params)
	if err != nil || len(frame) > 8<<20 {
		return nil, errors.New("validation configuration exceeds the local protocol limit")
	}
	helper, err := journalbridge.HelperPath()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(helper, "--validate")
	isolateSupervisor(cmd)
	helperEnv, err := nativeGitEnv()
	if err != nil {
		return nil, err
	}
	cmd.Env = append(helperEnv, "TMPDIR="+os.TempDir())
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer output.Close()
	if err = cmd.Start(); err != nil {
		return nil, errors.New("validation supervisor did not start; install the matching runner package")
	}
	// Cancellation and abrupt parent exit both close the control pipe. Never
	// kill the supervisor here: it owns group termination and direct-child reap.
	stop := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer stop()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(frame)))
	if _, err = input.Write(header[:]); err == nil {
		_, err = input.Write(frame)
	}
	if err != nil {
		_ = input.Close()
		_ = cmd.Wait()
		return nil, errors.New("validation launch frame was not delivered")
	}
	// One fixed-size receipt. Closing the output on overflow also unblocks
	// a mismatched helper attempting to stream arbitrary diagnostic output.
	receipt, readErr := io.ReadAll(io.LimitReader(output, 7))
	_ = output.Close()
	if len(receipt) != 6 {
		_ = input.Close()
	}
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil || len(receipt) != 6 || receipt[0] != 1 {
		return nil, errValidationUnconfirmed
	}
	code := int(int32(binary.BigEndian.Uint32(receipt[2:])))
	if code < -1 || code > 255 {
		return nil, errValidationUnconfirmed
	}
	switch receipt[1] {
	case 0: // Exited
		if code != 0 {
			return &code, fmt.Errorf("validation exited %d", code)
		}
		return &code, nil
	case 1: // Cancelled
		return &code, errors.New("validation interrupted")
	case 2: // TimedOut
		return &code, errors.New("validation timed out")
	case 3: // DidNotStart
		return nil, errors.New("validation executable did not start")
	case 4: // InvalidRequest
		return nil, errors.New("invalid validation configuration or incompatible supervisor")
	case 5: // ChangedWorktree
		return nil, errors.New("validation worktree or revision changed before launch")
	default:
		return nil, errValidationUnconfirmed
	}
}
