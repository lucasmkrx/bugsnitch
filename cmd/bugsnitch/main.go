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
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/device"
	"github.com/lucasmkrx/bugsnitch/internal/journalbridge"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

const usage = `Usage: bugsnitch COMMAND [OPTIONS]

  share-diff             Preview or share an approved candidate diff
  approval-check         Evaluate repository policy on an exact base and candidate
  approval-github        Evaluate authenticated GitHub PR or merge-group observations
  approval-review-receipt Prepare a check-run body for an independently reviewed candidate
  approval-preview       Replay repository policy against labeled historical changes
  doctor                 Check Git, native runner capabilities and Codex
  models                 List models and reasoning settings on this runtime
  usage                  Read available ChatGPT allowance windows
  proof                  Run a read-only ChatGPT authorization check
  pair, rotate, unpair, status    Manage local device enrollment
  local-run              Execute a bounded batch of approved issues locally
  local-unattended       Authorize and supervise a batch on this paired device
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
  local-verify           Verify issue criteria on an identified build or deployment
  local-verification     Inspect a saved verification receipt
  local-continue         Approve a plan or request a bounded correction
  local-show             Inspect the complete local stage evidence
  local-usage            Read metered model usage for an attempt
  local-ship             Publish an exact reviewed branch and open its owner-approved draft PR
  local-steward          Repair failed CI or open review feedback on a published candidate
  local-publish          Publish the reviewed candidate to its exact attempt branch
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
func readJSON(path string, v any) error { return readJSONLimit(path, v, 128<<10) }

func readJSONLimit(path string, v any, maxBytes int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return fmt.Errorf("input must be a regular JSON file no larger than %d bytes", maxBytes)
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
	case "local-unattended":
		return device.LocalUnattended(ctx, args[1:], os.Stdout)
	case "local-limits":
		return device.LocalLimits(args[1:], os.Stdout)
	case "local-usage":
		return localUsage(args[1:])
	case "share-diff":
		return device.Share(args[1:])
	case "next", "pair", "rotate", "unpair", "register", "sync", "status":
		return device.RunCLIContext(ctx, args)
	case "approval-github":
		return approvalGitHub(ctx, args[1:])
	case "approval-review-receipt":
		return approvalReceipt(ctx, args[1:])
	case "approval-check":
		return approvalCheck(ctx, args[1:])
	case "approval-preview":
		return approvalPreview(ctx, args[1:])
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
	case "local-verify":
		return localVerify(ctx, args[1:])
	case "local-verification":
		id := fs.String("id", "", "verification receipt ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || fs.NArg() != 0 {
			return errors.New("use local-verification --id RECEIPT_ID")
		}
		receipt, err := runner.GetVerification(*id)
		if err != nil {
			return err
		}
		return printJSON(receipt)
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
	case "local-ship":
		id := fs.String("id", "", "completed local attempt")
		version := fs.Int("version", 0, "completed attempt version")
		candidate := fs.String("candidate", "", "exact reviewed candidate SHA")
		target := fs.String("target", "", "owner-approved target branch")
		wait := fs.Duration("wait", 10*time.Minute, "time to wait for approval and GitHub (up to one hour)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		receipt, err := device.Ship(ctx, *id, *version, *candidate, *target, *wait, func(state string) {
			if state == "awaiting_approval" {
				fmt.Fprintln(os.Stderr, "Awaiting owner approval. Open this attempt in the dashboard, select the target and choose publication with local-ship.")
			} else {
				fmt.Fprintln(os.Stderr, "Draft delivery:", state)
			}
		})
		if err != nil {
			return err
		}
		return printJSON(receipt)
	case "local-steward":
		id := fs.String("id", "", "completed local attempt")
		version := fs.Int("version", 0, "reviewed attempt version")
		candidate := fs.String("candidate", "", "reviewed candidate SHA")
		n := fs.Int("pr", 0, "published pull request")
		watch := fs.Duration("watch", 0, "monitor this PR in the foreground for the duration")
		publish := fs.Bool("publish", false, "publish independently reviewed corrections with a PR undo note")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return steward(ctx, *id, *version, *candidate, *n, *watch, *publish)
	case "local-publish":
		id := fs.String("id", "", "completed attempt")
		note := fs.Bool("post-note", false, "post change and undo instructions on the corrected PR")
		version := fs.Int("version", 0, "reviewed attempt version")
		candidate := fs.String("candidate", "", "reviewed commit SHA")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		receipt, err := runner.PublishCandidate(ctx, *id, *version, *candidate)
		if err != nil {
			return err
		}
		if err = printJSON(receipt); err != nil {
			return err
		}
		if *note {
			return runner.PostStewardNote(ctx, *id, receipt)
		}
		return nil
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
		version := fs.Int("version", 0, "reviewed attempt version for local-continue")
		until := fs.Bool("until-candidate", false, "pause after committing the candidate and before validation")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *until && args[0] != "local-continue" {
			return errors.New("--until-candidate applies to local-continue")
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
		var a runner.Attempt
		var err error
		if *until {
			a, err = runner.ContinueToCandidate(ctx, *id, *feedback, *version)
		} else if *version != 0 {
			a, err = runner.ContinueAt(ctx, *id, *feedback, *version)
		} else {
			a, err = runner.Continue(ctx, *id, *feedback)
		}
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
	runner.MeasureWorker, runner.MeasureGeneral = device.MeasureWorker, device.MeasureGeneral
	runner.CheckWorker, runner.CheckPublication = device.CheckWorker, device.CheckPublication
	runner.BindGeneral, runner.SubmitGeneral = device.BindGeneral, device.SubmitGeneral
	runner.BeforeStart, runner.AfterFinish = device.Admit, device.Finish
	runner.BeforeTurn, runner.AfterTurn = device.TurnStart, device.TurnEnd
	signals := []os.Signal{os.Interrupt}
	// These commands own long-running work and must finish cleanup on SIGTERM.
	if len(os.Args) > 1 && (os.Args[1] == "sync" || os.Args[1] == "local-unattended" || os.Args[1] == "local-verify" && slices.Contains(os.Args[2:], "--")) {
		signals = append(signals, syscall.SIGTERM)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), signals...)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
