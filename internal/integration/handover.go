// SPDX-License-Identifier: MPL-2.0
package integration

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

type Handover struct {
	Version     int64  `json:"version"`
	MergedSHA   string `json:"mergedSha"`
	Target      string `json:"target"`
	Environment string `json:"environment"`
	Build       string `json:"build"`
	Digest      string `json:"evidenceDigest"`
	Summary     string `json:"summary"`
	TestSteps   string `json:"testSteps"`
}

type HandoverReceipt struct {
	Handover
	RecordedAt time.Time `json:"recordedAt"`
	RecordedBy string    `json:"recordedBy"`
}

var commitSHA = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func (s Store) Handover(ctx context.Context, w, p string, n int64, h Handover) error {
	role, id := audit.Actor(ctx)
	if (role != "owner" && role != "approver") || id == "" || n < 1 || h.Version < 1 ||
		!commitSHA.MatchString(h.MergedSHA) || !digest.MatchString(h.Digest) ||
		!handoverLabel(h.Target, 256) || !handoverLabel(h.Environment, 100) || !handoverLabel(h.Build, 256) ||
		strings.TrimSpace(h.Summary) == "" || len(h.Summary) > 2000 ||
		strings.TrimSpace(h.TestSteps) == "" || len(h.TestSteps) > 3000 {
		return ErrInvalid
	}
	h.Summary, h.TestSteps = incidents.Redact(h.Summary), incidents.Redact(h.TestSteps)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Use the exact inspected merge and destination shown for attestation.
	var matches bool
	err = tx.QueryRowContext(ctx, `SELECT coalesce(version=$4 AND snapshot->>'state'='MERGED'
	    AND snapshot->>'mergedSha'=$5 AND snapshot->>'target'=$6,false)
	    FROM pull_observations WHERE workspace_id=$1 AND project_id=$2 AND number=$3 FOR SHARE`,
		w, p, n, h.Version, h.MergedSHA, h.Target).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return ErrConflict
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO pull_handovers(
	    workspace_id,project_id,pull_number,inspection_version,merged_sha,target,environment,build,evidence_digest,summary,test_steps,actor_role,actor_id)
	    VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`,
		w, p, n, h.Version, h.MergedSHA, h.Target, h.Environment, h.Build, h.Digest, h.Summary, h.TestSteps, role, id)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		err = tx.QueryRowContext(ctx, `SELECT (inspection_version,merged_sha,target,evidence_digest,summary,test_steps,actor_role,actor_id)
		    =($6,$7,$8,$9,$10,$11,$12,$13)
		    FROM pull_handovers WHERE workspace_id=$1 AND project_id=$2 AND pull_number=$3 AND environment=$4 AND build=$5`,
			w, p, n, h.Environment, h.Build, h.Version, h.MergedSHA, h.Target, h.Digest, h.Summary, h.TestSteps, role, id).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches {
			return ErrConflict
		}
		return tx.Commit()
	}
	ctx = audit.WithDetails(ctx, audit.Details{
		Reason: "Deployment handover attested against an inspected merged pull request", SubjectVersion: h.Version, EvidenceRef: h.Digest,
	})
	if err = audit.Record(ctx, tx, w, p, "pull.handover_attested", strconv.FormatInt(n, 10)); err != nil {
		return err
	}
	return tx.Commit()
}

func handoverLabel(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit &&
		strings.IndexFunc(value, unicode.IsControl) < 0 && incidents.Redact(value) == value
}
