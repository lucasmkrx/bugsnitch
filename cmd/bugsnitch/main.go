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
	"github.com/lucasmkrx/bugsnitch/internal/journalbridge"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

const usage = `Usage: bugsnitch COMMAND [OPTIONS]

  share-diff             Preview or share an approved candidate diff
  doctor                 Check Git, native runner capabilities and Codex
  models                 List models and reasoning settings on this runtime
  usage                  Read available ChatGPT allowance windows
  proof                  Run a read-only ChatGPT authorization check
  pair, rotate, unpair, status    Manage local device enrollment
  local-run              Execute a bounded batch of approved issues locally
  local-limits           Show or set this runner's ticket and model-turn limits
  next --project ID      Print the next verified issue selection
  register               Map an assigned project to a local checkout
  sync [--watch]         Publish bounded local attempt metadata
  plan-draft             Draft a local proposal for new tickets
  manager                Ask the project manager a local read-only question
  manager-history        Inspect the local project conversation
  turns                  Show unfinished general model permits
  turn-reconcile         Reconcile a local general model permit
  project-add            Register the checkout, approved files and validator
  local-start            Start work on one owner-selected GitHub issue
  local-continue         Approve a plan or request a bounded correction
  local-show             Inspect the complete local stage evidence
  local-integrate-check  Validate a reviewed candidate with a selected target
  local-integrations     Inspect combined-check receipts
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
	case "local-run":
		return device.LocalRun(ctx, args[1:], os.Stdout)
	case "local-limits":
		return device.LocalLimits(args[1:], os.Stdout)
	case "share-diff":
		return device.Share(args[1:])
	case "next", "pair", "rotate", "unpair", "register", "sync", "status":
		return device.RunCLI(args)
	case "doctor":
		fmt.Printf("Platform %s/%s\n", runtime.GOOS, runtime.GOARCH)
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			return errors.New("local task execution requires macOS or Linux")
		}
		var missing bool
		helper, err := journalbridge.HelperPath()
		if err == nil {
			var output []byte
			output, err = exec.CommandContext(ctx, helper, "--version").Output()
			if err == nil {
				version := strings.TrimSpace(string(output))
				if strings.HasPrefix(version, "bugsnitch-runner-journal ") && strings.Contains(version, " protocol=1 sqlite=") && strings.HasSuffix(version, " capacity=1 execution=1 controls=1 attempts=1") {
					fmt.Println(version)
				} else {
					err = errors.New("native runner protocol mismatch")
				}
			}
		}
		if err != nil {
			fmt.Println("Native runner helper unavailable or incompatible. Install the matching runner bundle.")
			missing = true
		}
		for _, tool := range []string{"git", "codex"} {
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
			return fmt.Errorf("the runner requires its matching native helper, Git and Codex %s", agent.Version)
		}
		return nil
	case "models", "usage":
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		c, err := agent.Open(ctx)
		if err != nil {
			return err
		}
		defer c.Close()
		if args[0] == "usage" {
			u, e := c.Usage()
			if e != nil {
				return e
			}
			return printJSON(u)
		}
		if args[0] == "models" {
			m, err := c.Models()
			if err != nil {
				return err
			}
			return printJSON(m)
		}
		return errors.New("unknown runtime query")
	case "proof":
		r, err := runner.Proof(ctx)
		if err != nil {
			return err
		}
		if strings.TrimSpace(r.Text) != "BUGSNITCH_LOCAL_OK" {
			return errors.New("completed turn did not return the expected marker")
		}
		return printJSON(map[string]string{"runtime": agent.Version, "auth": "chatgpt", "status": "completed", "model": r.Model, "thread": r.Thread, "turn": r.Turn, "response": "BUGSNITCH_LOCAL_OK"})
	case "plan-draft":
		workspace := fs.String("workspace", "personal", "local workspace id")
		brief := fs.String("brief", "", "path to an owner-authored UTF-8 brief")
		model := fs.String("model", "", "optional discovered model")
		reason := fs.String("reasoning", "", "optional supported reasoning setting")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		f, err := os.Open(*brief)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 16385))
		if err != nil {
			return err
		}
		p, err := runner.DraftPlan(ctx, *workspace, string(b), *model, *reason)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Draft for owner review. Ticket numbers are provisional references within this proposal.")
		return printJSON(p)
	case "turns":
		rows, err := runner.GeneralSessions()
		if err != nil {
			return err
		}
		return printJSON(rows)
	case "turn-reconcile":
		id := fs.String("id", "", "general model permit ID")
		confirmed := fs.Bool("confirm-runtime-ended", false, "confirm independently verified termination on this computer")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return runner.ReconcileGeneral(*id, *confirmed)
	case "manager", "manager-history":
		ws := fs.String("workspace", "personal", "local workspace")
		repo := fs.String("repository", "", "registered owner/repository")
		id := fs.String("request-id", "", "stable 32-character hex ID for deduplication")
		file := fs.String("question", "", "local question file")
		model := fs.String("model", "", "optional model")
		effort := fs.String("reasoning", "", "optional reasoning setting")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if args[0] == "manager-history" {
			hist, err := runner.ManagerHistory(*ws, *repo)
			if err != nil {
				return err
			}
			return printJSON(hist)
		}
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		msg, err := io.ReadAll(io.LimitReader(f, 4097))
		if err != nil {
			return err
		}
		m, err := runner.Manager(ctx, *ws, *repo, *id, string(msg), *model, *effort)
		if m.ID != "" {
			if e := printJSON(m); e != nil {
				return e
			}
		}
		return err
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
	case "local-integrate-check", "local-integrations":
		id := fs.String("id", "", "reviewed local attempt")
		target := fs.String("target", "", "full local branch or origin tracking ref")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if args[0] == "local-integrations" {
			checks, err := runner.Checks(*id)
			if err != nil {
				return err
			}
			return printJSON(checks)
		}
		receipt, err := runner.CheckCombined(ctx, *id, *target)
		if receipt.ID != "" {
			if e := printJSON(receipt); e != nil {
				return e
			}
		}
		return err
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
	case "local-cleanup":
		id := fs.String("id", "", "local attempt id")
		check := fs.String("integration", "", "retained integration check id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *check != "" {
			return runner.CleanupIntegration(ctx, *id, *check)
		}
		return runner.Cleanup(*id)
	case "local-continue", "local-show", "local-recover":
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
	runner.CheckWorker = device.CheckWorker
	runner.BindGeneral, runner.SubmitGeneral = device.BindGeneral, device.SubmitGeneral
	runner.BeforeStart, runner.AfterFinish = device.Admit, device.Finish
	runner.BeforeTurn, runner.AfterTurn = device.TurnStart, device.TurnEnd
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
