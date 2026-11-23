//go:build darwin || linux

// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
)

// The test binary stands in for two generations of real validator processes.
// The grandchild keeps a kernel lock that disappears when it is terminated.
func TestValidatorProcessFixture(t *testing.T) {
	args := os.Args
	if len(args) < 3 || args[len(args)-2] != "--" {
		t.Skip("child process fixture")
	}
	switch args[len(args)-1] {
	case "spawn", "exit-first":
		cmd := exec.Command(args[0], "-test.run=^TestValidatorProcessFixture$", "--", "hold")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if args[len(args)-1] == "exit-first" {
			// Simulate a validator returning while another group member owns
			// a shared test resource. The supervisor still owns its teardown.
			waitValidationFile(t, "validator.ready")
			return
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	case "hold":
		lock, err := os.Create("validator.lock")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := lockFile(lock); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("validator.ready", []byte(os.Getenv("HOME")), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Second)
	case "run":
		data, err := os.ReadFile(os.Getenv("BUGSNITCH_VALIDATION_FIXTURE"))
		if err != nil {
			t.Fatal(err)
		}
		var a Attempt
		if err := json.Unmarshal(data, &a); err != nil {
			t.Fatal(err)
		}
		_, _ = validate(context.Background(), &a)
	default:
		t.Fatal("unknown process fixture")
	}
}

func validationAttempt(t *testing.T) Attempt {
	t.Helper()
	dir, issue := fixture(t)
	proj, err := project("personal", issue.Repository)
	if err != nil {
		t.Fatal(err)
	}
	state, err := home()
	if err != nil {
		t.Fatal(err)
	}
	sha, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	a := Attempt{Project: proj, Summary: Summary{ID: id, Repository: issue.Repository, BaseSHA: sha, CandidateSHA: sha},
		Worktree: filepath.Join(state, "worktrees", id), Branch: "bugsnitch/" + id}
	if err := a.prepare(); err != nil {
		t.Fatal(err)
	}
	return a
}

func waitValidationFile(t *testing.T, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	value, err := validationHandshake(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func validationHandshake(ctx context.Context, path string, exited <-chan struct{}) (string, error) {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) != 0 {
			return strings.TrimSpace(string(data)), nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-exited:
			return "", errors.New("worker exited before its validator handshake")
		case <-tick.C:
		}
	}
}

func TestValidationSupervisesDescendants(t *testing.T) {
	for _, name := range []string{"cancel", "timeout", "parent-death", "exit-first"} {
		t.Run(name, func(t *testing.T) {
			a := validationAttempt(t)
			fixtureMode := "spawn"
			if name == "exit-first" {
				fixtureMode = name
			}
			a.Project.Config.Validate = []string{os.Args[0], "-test.run=^TestValidatorProcessFixture$", "--", fixtureMode}
			a.Project.Config.ValidationTimeoutSeconds = 10
			if name == "timeout" {
				a.Project.Config.ValidationTimeoutSeconds = 2
			}
			var parent *exec.Cmd
			type result struct {
				code *int
				err  error
			}
			done := make(chan result, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "parent-death" {
				input := filepath.Join(t.TempDir(), "attempt.json")
				data, err := json.Marshal(a)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(input, data, 0600); err != nil {
					t.Fatal(err)
				}
				parent = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestValidatorProcessFixture$", "--", "run")
				parent.Env = append(os.Environ(), "BUGSNITCH_VALIDATION_FIXTURE="+input)
				if err := parent.Start(); err != nil {
					t.Fatal(err)
				}
				defer parent.Wait()
			} else {
				go func() { code, err := validate(ctx, &a); done <- result{code, err} }()
			}
			scratch := waitValidationFile(t, filepath.Join(a.Worktree, "validator.ready"))
			switch name {
			case "cancel":
				cancel()
			case "parent-death":
				if err := parent.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			if parent == nil {
				select {
				case r := <-done:
					if name == "exit-first" {
						if r.err != nil || r.code == nil || *r.code != 0 {
							t.Fatal("validator leader exit was lost", r)
						}
					} else if r.err == nil || r.code == nil || *r.code != -1 {
						t.Fatal("missing interrupted validator exit", r)
					}
					if name == "timeout" && !strings.Contains(r.err.Error(), "timed out") {
						t.Fatal(r.err)
					}
				case <-time.After(6 * time.Second):
					t.Fatal("validator failed to confirm interruption")
				}
			}
			// With the runner gone the Rust supervisor must finish cleanup itself.
			until := time.Now().Add(3 * time.Second)
			for time.Now().Before(until) {
				if _, err := os.Stat(scratch); errors.Is(err, os.ErrNotExist) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("private validator scratch retained", err)
			}
			lock, err := os.Open(filepath.Join(a.Worktree, "validator.lock"))
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := lockFile(lock); err != nil {
				t.Fatal("validator grandchild still holds its lock", err)
			}
		})
	}
}

func TestValidationRejectsChangedIdentity(t *testing.T) {
	a := validationAttempt(t)
	marker := filepath.Join(t.TempDir(), "unexpected")
	a.Project.Config.Validate = []string{"/bin/sh", "-c", "touch \"$1\"", "validator", marker}
	original := a.Worktree
	a.Worktree = a.Project.Path
	if code, err := validate(context.Background(), &a); err == nil || code != nil {
		t.Fatal("registered owner checkout accepted", code, err)
	}
	a.Worktree = original
	if _, err := git(original, "checkout", "-b", "reviewer"); err != nil {
		t.Fatal(err)
	}
	if code, err := validate(context.Background(), &a); err == nil || code != nil {
		t.Fatal("changed branch accepted", code, err)
	}
	if _, err := git(original, "checkout", a.Branch); err != nil {
		t.Fatal(err)
	}
	if _, err := git(original, "commit", "--allow-empty", "-m", "moved candidate"); err != nil {
		t.Fatal(err)
	}
	if code, err := validate(context.Background(), &a); err == nil || code != nil {
		t.Fatal("changed SHA accepted", code, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validator ran in a rejected checkout", err)
	}
}

func TestSupervisorLossPreservesPendingStop(t *testing.T) {
	dir, issue := fixture(t)
	signals := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	cfg := Config{Files: []string{"example.txt"}, Validate: []string{"/bin/sh", "-c",
		"echo \"$PPID\" > \"$1/supervisor\"; while test ! -f \"$1/release\"; do sleep 0.05; done", "validation-fixture", signals}}
	if err := AddProject("personal", issue.Repository, dir, cfg); err != nil {
		t.Fatal(err)
	}
	modelTurn = func(ctx context.Context, a *Attempt, _, _ string, _ any) (agent.Result, error) {
		if a.Phase == "plan" {
			return agent.Result{Text: "Fix the fixture"}, nil
		}
		return agent.Result{Text: `{"summary":"Updated fixture","edits":[{"path":"example.txt","content":"fixed\n"}]}`}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	a, err := Start(ctx, "personal", issue.Repository, issue)
	if err != nil {
		t.Fatal(err)
	}
	var released bool
	AfterFinish = func(Summary) error { released = true; return nil }
	done := make(chan struct{})
	var executionErr error
	go func() {
		defer close(done)
		_, executionErr = Continue(ctx, a.ID, "")
	}()
	release := func() { _ = os.WriteFile(filepath.Join(signals, "release"), nil, 0600) }
	defer func() {
		// Keep the fixture journal and the runner hooks installed until the
		// invocation has also exited on a failed handshake/assertion.
		release()
		cancel()
		<-done
	}()
	ready, err := validationHandshake(ctx, filepath.Join(signals, "supervisor"), done)
	if err != nil {
		select {
		case <-done:
			t.Fatalf("validator handshake: %v; worker: %v", err, executionErr)
		default:
			t.Fatal(err)
		}
	}
	pid, err := strconv.Atoi(ready)
	if err != nil || pid <= 1 {
		t.Fatal("fixture did not identify its own supervisor")
	}
	// Freeze the private supervisor before submitting the stop, then simulate
	// its loss. This prevents receipt timing from determining the test outcome.
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	frozen := true
	defer func() {
		if frozen {
			_ = syscall.Kill(pid, syscall.SIGCONT)
		}
	}()
	if status, err := RequestControl(a.ID, "stop"); err != nil || status != "received" {
		t.Fatal(status, err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	frozen = false
	release()
	<-done
	if !errors.Is(executionErr, errValidationUnconfirmed) {
		t.Fatal("unconfirmed supervisor outcome was lost", executionErr)
	}
	current, err := Get(a.ID)
	if err != nil || current.State != "running" || current.Phase != "validate" || released {
		t.Fatal("lost supervision released ownership", current.Summary, err)
	}
	if op, err := journalControl(context.Background(), controlRequest{Action: "pending", ID: a.ID}); err != nil || op != "stop" {
		t.Fatal("an unconfirmed stop was acknowledged", op, err)
	}
}
