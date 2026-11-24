// SPDX-License-Identifier: MPL-2.0
package main

import (
	"errors"
	"flag"
	"sort"

	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

type modelRate struct {
	InputPerMillionUSD  *float64 `json:"inputPerMillionUSD"`
	OutputPerMillionUSD *float64 `json:"outputPerMillionUSD"`
}

type modelTotal struct {
	Model        string   `json:"model"`
	Turns        int      `json:"meteredTurns"`
	InputTokens  int64    `json:"inputTokens"`
	OutputTokens int64    `json:"outputTokens"`
	EstimatedUSD *float64 `json:"estimatedUSD,omitempty"`
}

type usageReport struct {
	Attempt        string       `json:"attemptId"`
	UnmeteredTurns int          `json:"unmeteredTurns"`
	Models         []modelTotal `json:"models"`
}

func localUsage(args []string) error {
	fs := flag.NewFlagSet("local-usage", flag.ContinueOnError)
	id := fs.String("id", "", "local attempt ID")
	rateFile := fs.String("rates", "", "JSON prices in USD per million tokens, keyed by exact model ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || fs.NArg() != 0 {
		return errors.New("use local-usage --id ATTEMPT_ID [--rates RATES.json]")
	}
	var rates map[string]modelRate
	if *rateFile != "" {
		if err := readJSON(*rateFile, &rates); err != nil {
			return err
		}
		for _, rate := range rates {
			if rate.InputPerMillionUSD == nil || rate.OutputPerMillionUSD == nil ||
				*rate.InputPerMillionUSD < 0 || *rate.OutputPerMillionUSD < 0 ||
				*rate.InputPerMillionUSD > 100_000 || *rate.OutputPerMillionUSD > 100_000 {
				return errors.New("specify input and output prices from 0 to 100,000 USD per million tokens for each model")
			}
		}
	}
	attempt, err := runner.Get(*id)
	if err != nil {
		return err
	}
	return printJSON(summarizeUsage(attempt, rates))
}

func summarizeUsage(attempt runner.Attempt, rates map[string]modelRate) usageReport {
	report := usageReport{Attempt: attempt.ID, Models: []modelTotal{}}
	byModel := make(map[string]modelTotal)
	for _, artifact := range attempt.Artifacts {
		if artifact.Turn == "" {
			continue
		}
		usage := artifact.Usage
		if usage == nil || artifact.Model == "" {
			report.UnmeteredTurns++
			continue
		}
		total := byModel[artifact.Model]
		total.Model = artifact.Model
		total.Turns++
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		byModel[artifact.Model] = total
	}
	for model, total := range byModel {
		if rate, ok := rates[model]; ok {
			inputPrice, outputPrice := *rate.InputPerMillionUSD, *rate.OutputPerMillionUSD
			price := (float64(total.InputTokens)*inputPrice + float64(total.OutputTokens)*outputPrice) / 1e6
			total.EstimatedUSD = &price
		}
		report.Models = append(report.Models, total)
	}
	sort.Slice(report.Models, func(i, j int) bool { return report.Models[i].Model < report.Models[j].Model })
	return report
}
