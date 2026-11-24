// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/approvalpolicy"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func approvalCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("approval-check", flag.ContinueOnError)
	repo := fs.String("repo", ".", "local Git checkout")
	base := fs.String("base", "", "trusted target commit SHA")
	head := fs.String("head", "", "candidate commit SHA")
	receipt := fs.String("evidence", "", "observed evidence JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *base == "" || *head == "" || fs.NArg() != 0 {
		return errors.New("provide --base and --head as immutable commit SHAs")
	}
	var e approvalpolicy.Evidence
	if *receipt != "" {
		if err := readJSON(*receipt, &e); err != nil {
			return err
		}
	}
	d := approvalpolicy.Inspect(ctx, *repo, *base, *head, e)
	if err := printJSON(d); err != nil {
		return err
	}
	if d.Outcome != "would_allow" {
		return errors.New("approval policy held the candidate")
	}
	return nil
}

func approvalPreview(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("approval-preview", flag.ContinueOnError)
	repo := fs.String("repo", ".", "local Git checkout")
	cases := fs.String("cases", "", "file of labeled historical changes")
	policySHA := fs.String("policy-revision", "", "immutable trusted policy to simulate on ordinary historical changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cases == "" || fs.NArg() != 0 {
		return errors.New("provide historical --cases JSON")
	}
	var data approvalpolicy.CaseFile
	if err := readJSONLimit(*cases, &data, 4<<20); err != nil {
		return err
	}
	report, err := approvalpolicy.Replay(ctx, *repo, *policySHA, data)
	if err != nil {
		return err
	}
	return printJSON(report)
}

// approvalGitHub reads only authenticated GitHub observations. Run this binary
// from a trusted installation outside the checkout being evaluated.
func approvalGitHub(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("approval-github", flag.ContinueOnError)
	repo := fs.String("repo", ".", "local Git object store")
	eventFile := fs.String("event", "", "GitHub event JSON")
	wait := fs.Duration("wait", 0, "bounded time to wait for independent checks and reviews")
	var registration approvalpolicy.Registration
	fs.StringVar(&registration.Repository, "repository", "", "enrolled owner/repository")
	fs.Int64Var(&registration.RepositoryID, "repository-id", 0, "enrolled numeric repository ID")
	fs.StringVar(&registration.Target, "target", "", "enrolled target branch")
	fs.Int64Var(&registration.ReviewApp, "review-app", 0, "trusted independent reviewer GitHub App ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *wait < 0 || *wait > 30*time.Minute {
		return errors.New("--wait must be between zero and 30m")
	}
	if *eventFile == "" || fs.NArg() != 0 {
		return errors.New("provide --event and the trusted repository registration")
	}
	f, err := os.Open(*eventFile)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	var event approvalpolicy.Event
	if err != nil || len(b) > 1<<20 || json.Unmarshal(b, &event) != nil {
		return errors.New("invalid or oversized GitHub event")
	}
	token := os.Getenv("BUGSNITCH_GITHUB_READ_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		return errors.New("provide a read-scoped GitHub token")
	}
	deadline := time.Now().Add(*wait)
	d := approvalpolicy.ObserveGitHub(ctx, *repo, registration, event, token)
	for d.Outcome != "would_allow" && time.Now().Before(deadline) {
		timer := time.NewTimer(min(15*time.Second, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		d = approvalpolicy.ObserveGitHub(ctx, *repo, registration, event, token)
	}
	if err := printJSON(d); err != nil {
		return err
	}
	if d.Outcome != "would_allow" {
		return errors.New("GitHub approval check held the candidate")
	}
	return nil
}

// approvalReceipt prepares a check-run request from completed local evidence.
// Publication and the review App's credentials belong to the trusted publisher.
func approvalReceipt(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("approval-review-receipt", flag.ContinueOnError)
	id := fs.String("id", "", "completed local attempt")
	version := fs.Int("version", 0, "exact attempt version")
	var s approvalpolicy.PullSubject
	fs.StringVar(&s.Repository, "repository", "", "trusted repository name")
	fs.Int64Var(&s.RepositoryID, "repository-id", 0, "trusted numeric repository ID")
	fs.IntVar(&s.PR, "pr", 0, "PR number")
	fs.StringVar(&s.Target, "target", "", "target branch")
	fs.StringVar(&s.Base, "base", "", "target SHA reviewed by this attempt")
	fs.StringVar(&s.Head, "head", "", "candidate SHA reviewed independently")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected receipt arguments")
	}
	a, err := runner.Get(*id)
	if err != nil {
		return err
	}
	if !strings.EqualFold(a.Repository, s.Repository) || s.Head != a.CandidateSHA || a.Version != *version || a.State != "completed" || a.Phase != "wrap_up" {
		return errors.New("receipt requires the completed local attempt for this exact repository and head")
	}
	reviewBase := a.BaseSHA
	if a.Steward != nil {
		reviewBase = a.Steward.OriginalBase
		if a.Steward.Pull != s.PR || a.Steward.Target != s.Target {
			return errors.New("receipt differs from the reviewed pull request")
		}
	}
	if s.Base != reviewBase {
		return errors.New("target no longer matches the base of the reviewed diff")
	}
	depth, artifact, err := a.PassingReview(reviewBase)
	if err != nil {
		return err
	}
	body, err := approvalpolicy.ReviewRequest(s, depth)
	if err != nil {
		return err
	}
	body["output"] = map[string]string{"title": "Independent local review", "summary": "Validated and independently reviewed local candidate. Retained artifact SHA-256: " + artifact}
	return printJSON(body)
}
