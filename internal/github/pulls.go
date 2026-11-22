// SPDX-License-Identifier: MPL-2.0
package github

import (
	"context"
	"errors"
)

type PullObservation struct {
	Queue      *QueueObservation `json:"queue,omitempty"`
	MergedSHA  string            `json:"mergedSha,omitempty"`
	Title      string            `json:"title"`
	State      string            `json:"state"`
	IssueState string            `json:"issueState"`
	Head       string            `json:"head"`
	Base       string            `json:"base"`
	Target     string            `json:"target"`
	Criteria   string            `json:"criteria"`
	Checks     string            `json:"checks"`
	CheckCount int               `json:"checkCount"`
	Review     string            `json:"review"`
	Draft      bool              `json:"draft"`
}

func (a *App) InspectPull(ctx context.Context, install, repo int64, name string, number, issue int64) (PullObservation, error) {
	var out PullObservation
	if number < 1 || number > 2147483647 || issue < 1 || issue > 2147483647 || number == issue {
		return out, errors.New("invalid issue or pull request number")
	}
	r, err := a.open(ctx, install, repo, name, "inspection")
	if err != nil {
		return out, err
	}
	defer r.Close()
	err = r.call(ctx, fields{"op": "inspect", "number": number, "issue": issue}, &out)
	return out, err
}
