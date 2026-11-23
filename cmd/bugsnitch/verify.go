// SPDX-License-Identifier: MPL-2.0
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func localVerify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("local-verify", flag.ContinueOnError)
	selection := fs.String("issue", "", "reviewed versioned issue selection JSON")
	repo := fs.String("repository", "", "GitHub owner/repository")
	number := fs.Int("number", 0, "open GitHub issue number")
	criteria := fs.String("criteria", "", "reviewed acceptance criteria as a JSON string array")
	env := fs.String("environment", "", "observed deployment environment")
	revision := fs.String("revision", "", "tested immutable build, commit or deployment revision")
	observations := fs.String("observations", "", "observations JSON file, or - for stdin")
	source := fs.String("source", "", "origin of imported observations (operator or check)")
	observer := fs.String("observer", "", "person or check that made the observations")
	// Only a literal -- introduces a command. An accidental positional
	// argument must never become the program a verification run launches.
	var check []string
	if i := slices.Index(args, "--"); i >= 0 {
		check = args[i+1:]
		if len(check) == 0 || !filepath.IsAbs(check[0]) {
			return errors.New("provide an absolute check program path after --")
		}
		args = args[:i]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional argument to local-verify")
	}
	issue := runner.Issue{Repository: *repo, Number: *number}
	if *selection != "" {
		if *repo != "" || *number != 0 || *criteria != "" {
			return errors.New("select --issue or --repository with --number and --criteria")
		}
		if err := readJSON(*selection, &issue); err != nil {
			return err
		}
		if issue.Schema != 1 {
			return errors.New("use a version 1 reviewed issue selection")
		}
	} else {
		if *repo == "" || *number <= 0 || *criteria == "" {
			return errors.New("use --issue or select --repository, --number and --criteria")
		}
		if err := readJSON(*criteria, &issue.Criteria); err != nil {
			return err
		}
	}
	input := runner.VerificationInput{Environment: *env, Revision: *revision, Observer: *observer}
	if len(check) != 0 {
		if *observations != "" || *source != "" {
			return errors.New("a directly executed check collects its own observations")
		}
		input.Source = "runner-check"
		if input.Observer == "" {
			input.Observer = filepath.Base(check[0])
		}
	} else {
		if *observations == "" {
			return errors.New("provide --observations or an absolute check program after --")
		}
		switch *source {
		case "", "operator":
			input.Source = "operator"
			if input.Observer == "" {
				input.Observer = "operator"
			}
		case "check":
			input.Source = "imported-check"
		default:
			return errors.New("imported observation source must be operator or check")
		}
		if *observations == "-" {
			data, err := io.ReadAll(io.LimitReader(os.Stdin, (128<<10)+1))
			if err != nil || len(data) > 128<<10 {
				return errors.New("provide at most 128 KiB of observations on stdin")
			}
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if dec.Decode(&input.Observations) != nil || dec.Decode(&struct{}{}) != io.EOF {
				return errors.New("invalid observations JSON on stdin")
			}
		} else if err := readJSON(*observations, &input.Observations); err != nil {
			return err
		}
	}
	receipt, err := runner.RecordVerification(ctx, issue, input, check)
	if err != nil {
		return err
	}
	return printJSON(receipt)
}
