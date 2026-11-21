// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/device"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

const usage = `Usage: bugsnitch COMMAND [OPTIONS]

  doctor                 Check local Git, SQLite and Codex versions
  models                 List models and reasoning settings on this runtime
  proof                  Run a read-only ChatGPT authorization check
  pair, unpair, status    Manage local device enrollment
  register               Map an assigned project to a local checkout
  sync [--watch]         Publish bounded local attempt metadata
  project-add            Register the checkout, approved files and validator
  local-start            Start work on one owner-selected GitHub issue
  local-continue         Approve a plan or request a bounded correction
  local-show             Inspect the complete local stage evidence
  local-list             Show local attempt summaries
  local-recover          Reconcile a committed candidate after an interrupted write
  local-cleanup          Remove an owned, clean, terminal worktree
  local-retry            Start a distinct bounded retry of a terminal failure`

func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<10 {
		return errors.New("input must be a regular JSON file under 128 KiB")
	}
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err = dec.Decode(v); err != nil {
		return errors.New("invalid JSON document; check required fields")
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println(usage)
		return nil
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "pair", "unpair", "register", "sync", "status":
		return device.RunCLI(args)
	case "doctor":
		fmt.Printf("Platform %s/%s\n", runtime.GOOS, runtime.GOARCH)
		var missing bool
		for _, tool := range []string{"git", "sqlite3", "codex"} {
			cmd := exec.CommandContext(ctx, tool, "--version")
			out, err := cmd.Output()
			if err != nil {
				fmt.Printf("%s unavailable\n", tool)
				missing = true
				continue
			}
			line := strings.TrimSpace(string(out))
			fmt.Printf("%s\n", line)
			if tool == "codex" && line != "codex-cli "+agent.Version {
				missing = true
			}
		}
		if missing {
			return fmt.Errorf("install Git, SQLite CLI and Codex %s on PATH", agent.Version)
		}
		return nil
	case "models", "proof":
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		c, err := agent.Open(ctx)
		if err != nil {
			return err
		}
		defer c.Close()
		if args[0] == "models" {
			m, err := c.Models()
			if err != nil {
				return err
			}
			return printJSON(m)
		}
		r, err := c.Turn("Respond only with the requested marker. Use no tools.", "Reply with exactly BUGSNITCH_LOCAL_OK.", "", "", nil)
		if err != nil {
			return err
		}
		if strings.TrimSpace(r.Text) != "BUGSNITCH_LOCAL_OK" {
			return errors.New("completed turn did not return the expected marker")
		}
		return printJSON(map[string]string{"runtime": agent.Version, "auth": "chatgpt", "status": "completed", "model": r.Model, "thread": r.Thread, "turn": r.Turn, "response": "BUGSNITCH_LOCAL_OK"})
	case "project-add":
		w := fs.String("workspace", "personal", "workspace id")
		r := fs.String("repository", "", "GitHub owner/repository")
		p := fs.String("path", "", "canonical checkout")
		c := fs.String("config", "", "reviewed execution config JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var conf runner.Config
		if err := readJSON(*c, &conf); err != nil {
			return err
		}
		return runner.AddProject(*w, *r, *p, conf)
	case "local-retry":
		id := fs.String("id", "", "failed or cancelled attempt")
		i := fs.String("issue", "", "current reviewed issue selection")
		reason := fs.String("reason", "", "reason for a new attempt")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var issue runner.Issue
		if err := readJSON(*i, &issue); err != nil {
			return err
		}
		a, err := runner.Retry(ctx, *id, issue, *reason)
		if a.ID != "" {
			if e := printJSON(a.Summary); e != nil {
				return e
			}
		}
		return err
	case "local-start":
		w := fs.String("workspace", "personal", "workspace id")
		r := fs.String("repository", "", "GitHub owner/repository")
		i := fs.String("issue", "", "reviewed issue selection JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var issue runner.Issue
		if err := readJSON(*i, &issue); err != nil {
			return err
		}
		a, err := runner.Start(ctx, *w, *r, issue)
		if a.ID != "" {
			if e := printJSON(a.Summary); e != nil {
				return e
			}
		}
		return err
	case "local-continue", "local-show", "local-recover", "local-cleanup":
		id := fs.String("id", "", "local attempt id")
		feedback := fs.String("feedback", "", "feedback for the worker")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if args[0] == "local-recover" {
			a, e := runner.RecoverCommit(*id)
			if e != nil {
				return e
			}
			return printJSON(a.Summary)
		}
		if args[0] == "local-cleanup" {
			return runner.Cleanup(*id)
		}
		if args[0] == "local-show" {
			a, err := runner.Get(*id)
			if err != nil {
				return err
			}
			return printJSON(a)
		}
		a, err := runner.Continue(ctx, *id, *feedback)
		if a.ID != "" {
			if e := printJSON(a.Summary); e != nil {
				return e
			}
		}
		return err
	case "local-list":
		s, err := runner.Summaries()
		if err != nil {
			return err
		}
		return printJSON(s)
	}
	return fmt.Errorf("unknown command %q; run bugsnitch help", args[0])
}
func main() {
	runner.BeforeStart, runner.AfterFinish = device.Admit, device.Finish
	runner.BeforeTurn, runner.AfterTurn = device.TurnStart, device.TurnEnd
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
