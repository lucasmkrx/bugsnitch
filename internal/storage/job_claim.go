// SPDX-License-Identifier: MPL-2.0
package storage

import (
	"context"
	"database/sql"
)

// A worker must lock this scoped row at Fence before its first provider read.
// If the native claim response is lost, the queue's original retry timestamp
// makes the job eligible again after the lease. No external write is implied.
type JobClaim struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	Item      string `json:"item"`
	Number    int64  `json:"number"`
	Fence     string `json:"fence"`
}

func ClaimJob(ctx context.Context, db *sql.DB, queue string) (JobClaim, error) {
	var claim *JobClaim
	err := Coordinate(ctx, db, "jobs.claim_"+queue, struct{}{}, &claim)
	if err != nil {
		return JobClaim{}, err
	}
	if claim == nil {
		return JobClaim{}, sql.ErrNoRows
	}
	return *claim, nil
}
