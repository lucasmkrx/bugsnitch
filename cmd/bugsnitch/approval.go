// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"errors"
	"flag"

	"github.com/lucasmkrx/bugsnitch/internal/approvalpolicy"
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
