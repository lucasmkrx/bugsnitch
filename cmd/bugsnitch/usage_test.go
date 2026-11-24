// SPDX-License-Identifier: MPL-2.0
package main

import (
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func TestUsageSeparatesUnmeteredTurnsAndPricesObservedTokens(t *testing.T) {
	attempt := runner.Attempt{Artifacts: []runner.Artifact{
		{Model: "claude-review", Turn: "review-1", Usage: &agent.ModelUsage{InputTokens: 3_000, OutputTokens: 100}},
		{Model: "claude-review", Turn: "review-2", Usage: &agent.ModelUsage{InputTokens: 7_000, OutputTokens: 900}},
		{Model: "codex", Turn: "worker"},
		{Phase: "validate"},
	}}
	input, output := 2.0, 10.0
	got := summarizeUsage(attempt, map[string]modelRate{
		"claude-review": {InputPerMillionUSD: &input, OutputPerMillionUSD: &output},
	})
	if len(got.Models) != 1 || got.UnmeteredTurns != 1 {
		t.Fatal(got)
	}
	model := got.Models[0]
	if model.Turns != 2 || model.InputTokens != 10_000 || model.OutputTokens != 1_000 ||
		model.EstimatedUSD == nil || *model.EstimatedUSD != 0.03 {
		t.Fatal(model)
	}
}
