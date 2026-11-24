// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/approvalpolicy"
)

func reviewLevel(depth string) int {
	switch depth {
	case "quick":
		return 1
	case "standard":
		return 2
	case "deep":
		return 3
	}
	return 0
}

func candidateReviewDepth(ctx context.Context, a *Attempt, base string) (string, error) {
	depth := a.Project.Config.ReviewDepth
	entry, err := git(a.Worktree, "ls-tree", base, "--", approvalpolicy.File)
	if err != nil {
		return "", err
	}
	if entry != "" {
		decision := approvalpolicy.Inspect(ctx, a.Worktree, base, a.CandidateSHA, approvalpolicy.Evidence{})
		required := decision.Required.ReviewDepth
		if !decision.RequirementsResolved || reviewLevel(required) == 0 {
			return "", errors.New("cannot determine review depth from the target's policy: " + strings.Join(decision.Reasons, ", "))
		}
		if reviewLevel(required) > reviewLevel(depth) {
			depth = required
		}
	}
	if depth == "" {
		depth = "standard"
	}
	return depth, nil
}

func runReview(ctx context.Context, a *Attempt, instruction, depth, base string, payload map[string]any, validation Artifact) (Artifact, error) {
	result, err := turn(ctx, a, directive+" "+instruction, payload, reviewSchema)
	if err != nil {
		return Artifact{}, err
	}
	var report review
	if json.Unmarshal([]byte(result.Text), &report) != nil || strings.TrimSpace(report.Summary) == "" || len(report.Findings) > 64 {
		return Artifact{}, errors.New("invalid review artifact")
	}
	for _, f := range report.Findings {
		if f.Location == "" || f.Rationale == "" || (f.Severity != "important" && f.Severity != "minor") {
			return Artifact{}, errors.New("incomplete review finding")
		}
	}
	out := recordTurn(a, result, report.Summary)
	out.Findings, out.ReviewDepth, out.ReviewBase, out.ValidationID = report.Findings, depth, base, validation.ID
	return out, nil
}

func blockingReview(a Artifact) bool {
	for _, f := range a.Findings {
		if f.Severity == "important" {
			return true
		}
	}
	return false
}

// PassingReview identifies the receipt a trusted publisher can bind to a pull request.
func (a Attempt) PassingReview(base string) (string, string, error) {
	var worker, validation, depth, receipt string
	firstPass := map[string]string{}
	for _, f := range a.Artifacts {
		if f.CandidateSHA != a.CandidateSHA {
			continue
		}
		switch f.Phase {
		case "implement":
			worker, validation, receipt = f.Thread, "", ""
			clear(firstPass)
		case "validate":
			validation, receipt = "", ""
			clear(firstPass)
			if f.ExitCode != nil && *f.ExitCode == 0 {
				validation = f.ID
			}
		case "review":
			receipt = ""
			if worker == "" || validation == "" || f.ID == "" || f.Model == "" || f.Thread == "" || f.Thread == worker || f.Turn == "" || blockingReview(f) {
				continue
			}
			depth = f.ReviewDepth
			if depth == "" {
				// Legacy completed attempts used the single standard review.
				depth = "standard"
			} else if reviewLevel(depth) == 0 || f.ReviewBase != base || f.ValidationID != validation {
				continue
			}
			if depth == "deep" {
				first := firstPass[f.SecondReviewOf]
				if first == "" || first == f.Thread {
					continue
				}
			} else if depth == "standard" {
				firstPass[f.ID] = f.Thread
			}
			receipt = f.ID
		}
	}
	if receipt == "" {
		return "", "", errors.New("independent passing local validation and review receipts are required")
	}
	return depth, receipt, nil
}

func inspectCandidate(ctx context.Context, a *Attempt, base, diff string, validated Artifact) (review, bool, error) {
	depth, err := candidateReviewDepth(ctx, a, base)
	if err != nil {
		return review{}, false, err
	}
	payload := map[string]any{"issue": a.Issue, "diff": diff, "evidence": validated}
	instruction := "Independently inspect the actual diff against the issue and the validation result. Identify defects, missing acceptance coverage and interactions with existing behavior. Report important findings that should block readiness."
	if depth == "quick" {
		instruction = "Independently check the diff against the requested behavior and validation result. Report remaining defects and any acceptance criterion without evidence."
	}
	var first Artifact
	if depth == "deep" {
		// Resume only after the same validation on the same pair of commits.
		for _, f := range a.Artifacts {
			if f.Phase == "review" && f.ReviewDepth == "standard" && f.ValidationID == validated.ID && f.CandidateSHA == a.CandidateSHA && f.ReviewBase == base {
				first = f
			}
		}
	}
	if first.ID == "" {
		firstDepth := depth
		if depth == "deep" {
			firstDepth = "standard"
		}
		first, err = runReview(ctx, a, instruction, firstDepth, base, payload, validated)
		if err != nil {
			return review{}, false, err
		}
		a.add(first)
		first = a.Artifacts[len(a.Artifacts)-1]
	}
	report := review{Summary: first.Summary, Findings: first.Findings}
	if depth != "deep" || blockingReview(first) {
		return report, blockingReview(first), nil
	}
	if first.Thread == "" || first.Turn == "" {
		return review{}, false, errors.New("deep review requires attributable independent review turns")
	}
	if err = a.save(); err != nil {
		return review{}, false, err
	}
	// Give the second reviewer the original evidence without the first verdict.
	second, err := runReview(ctx, a, "Independently test the reasoning behind this change. Examine boundary inputs, integration and compatibility, retry and failure behavior, and whether the validator would catch a regression. Record concrete findings tied to the actual diff.", "deep", base, payload, validated)
	if err != nil {
		return review{}, false, err
	}
	if second.Thread == "" || second.Turn == "" || second.Thread == first.Thread {
		return review{}, false, errors.New("deep review requires two distinct review threads")
	}
	for _, f := range a.Artifacts {
		if f.Phase == "implement" && f.CandidateSHA == a.CandidateSHA && (f.Thread == first.Thread || f.Thread == second.Thread) {
			return review{}, false, errors.New("implementation and deep review threads must be independent")
		}
	}
	second.SecondReviewOf = first.ID
	a.add(second)
	report.Summary += "\n\n" + second.Summary
	report.Findings = append(append([]Finding{}, report.Findings...), second.Findings...)
	return report, blockingReview(second), nil
}
