// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lucasmkrx/bugsnitch/internal/linear"
	"strings"
	"time"
)

func (s *Server) receiveLinear(ctx context.Context, input []byte) (any, error) {
	if s.LinearAgent == nil {
		return nil, problem{503, "Configure the Linear agent identity first"}
	}
	var b struct{ Session, Issue, Prompt string }
	if json.Unmarshal(input, &b) != nil || !linear.ValidUUID(b.Session) || !linear.ValidUUID(b.Issue) || (b.Prompt != "" && !linear.ValidUUID(b.Prompt)) {
		return nil, bad("Invalid Linear session")
	}
	// Unique prompt receipts prevent an old redelivery from waking a completed newer turn.
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var ws, p string
		var n int64
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT workspace_id,project_id,issue_number,count(*) OVER () FROM linear_issue_links WHERE linear_id=$1 LIMIT 1", b.Issue).Scan(&ws, &p, &n, &count); err != nil {
			return err
		}
		if count != 1 {
			return conflict("Linear issue must map to one workspace")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO linear_sessions(id,workspace_id,project_id,issue_number) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, b.Session, ws, p, n)
		if err != nil {
			return err
		}
		var matches bool
		if err = tx.QueryRowContext(ctx, "SELECT (workspace_id,project_id,issue_number)=($2,$3,$4) FROM linear_sessions WHERE id=$1 FOR UPDATE", b.Session, ws, p, n).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return denied
		}
		if b.Prompt != "" {
			res, err := tx.ExecContext(ctx, "INSERT INTO linear_prompts(session_id,id) VALUES($1,$2) ON CONFLICT DO NOTHING", b.Session, b.Prompt)
			if err != nil {
				return err
			}
			rows, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if rows == 1 {
				_, err = tx.ExecContext(ctx, "UPDATE linear_sessions SET latest_prompt=$2,finished=false,failures=0,due_at=now() WHERE id=$1", b.Session, b.Prompt)
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return object{"accepted": true}, nil
}
func (s *Server) ProcessLinearSessions(ctx context.Context) {
	for ctx.Err() == nil {
		processed, err := s.ProcessLinearSession(ctx)
		if err == nil && processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func (s *Server) ProcessLinearSession(ctx context.Context) (bool, error) {
	var found bool
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var id, ws, p, repo, prompt, previous string
		var n int
		var since time.Time
		err := tx.QueryRowContext(ctx, `SELECT s.id,s.workspace_id,s.project_id,p.repository,s.issue_number,s.created_at,coalesce(s.latest_prompt::text,''),coalesce(s.last_digest,'')
      FROM linear_sessions s JOIN projects p ON (p.workspace_id,p.id)=(s.workspace_id,s.project_id)
      WHERE NOT s.finished AND s.due_at<=now() ORDER BY s.due_at,s.id LIMIT 1 FOR UPDATE OF s SKIP LOCKED`).Scan(&id, &ws, &p, &repo, &n, &since, &prompt, &previous)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		var evidence string
		err = tx.QueryRowContext(ctx, `SELECT h.environment||' build '||h.build||' (merge '||h.merged_sha||', evidence '||h.evidence_digest||')'
      FROM pull_observations o JOIN pull_handovers h ON (h.workspace_id,h.project_id,h.pull_number,h.merged_sha,h.target)=(o.workspace_id,o.project_id,o.number,o.snapshot->>'mergedSha',o.snapshot->>'target')
      WHERE o.workspace_id=$1 AND o.project_id=$2 AND o.issue_number=$3 AND o.snapshot->>'state'='MERGED' AND h.recorded_at>=$4 ORDER BY h.recorded_at DESC LIMIT 1`, ws, p, n, since).Scan(&evidence)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		kind := "thought"
		body := fmt.Sprintf("Delegated %s#%d to Bugsnitch. An eligible registered runner can take it from its approved plan.", repo, n)
		var state, phase string
		if evidence != "" {
			kind = "response"
			var admitted, metered, priced, input, output int64
			var dollars sql.NullFloat64
			err = tx.QueryRowContext(ctx, `SELECT count(*),count(t.model),count(t.estimated_usd),coalesce(sum(t.input_tokens),0),coalesce(sum(t.output_tokens),0),sum(t.estimated_usd)
                FROM priced_model_turns t JOIN attempt_reports a ON (a.workspace_id,a.project_id,a.id)=(t.workspace_id,t.project_id,t.subject_id)
                WHERE t.workspace_id=$1 AND t.project_id=$2 AND t.kind='admission.turn_start' AND a.issue_number=$3`, ws, p, n).Scan(&admitted, &metered, &priced, &input, &output, &dollars)
			if err != nil {
				return err
			}
			body = fmt.Sprintf("Deployment handover recorded for %s#%d: %s. Metering covers %d of %d admitted worker turns (%d input and %d output tokens).", repo, n, evidence, metered, admitted, input, output)
			if dollars.Valid {
				body += fmt.Sprintf(" Estimated model cost USD %.4f for %d priced turns.", dollars.Float64, priced)
			}
		} else {
			e := tx.QueryRowContext(ctx, `SELECT state,phase FROM attempt_reports WHERE workspace_id=$1 AND project_id=$2 AND issue_number=$3 ORDER BY updated_at DESC,id LIMIT 1`, ws, p, n).Scan(&state, &phase)
			if e == nil {
				body = fmt.Sprintf("%s#%d · runner %s at %s. Awaiting deployment handover.", repo, n, state, phase)
			} else if e != sql.ErrNoRows {
				return e
			}
		}
		digest := sha256.Sum256([]byte(strings.Join([]string{id, prompt, kind, body}, "\n")))
		raw := digest[:16]
		raw[6] = raw[6]&15 | 64
		raw[8] = raw[8]&63 | 128
		activity := fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
		if previous != activity {
			if err = s.LinearAgent.Activity(ctx, id, activity, kind, body); err != nil {
				_, err = tx.ExecContext(ctx, `UPDATE linear_sessions SET failures=least(failures+1,100),due_at=now()+ make_interval(secs=>least(3600,10*power(2,least(failures,9))::int)) WHERE id=$1`, id)
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE linear_sessions SET last_digest=$2,failures=0,finished=$3,due_at=now()+interval '30 seconds' WHERE id=$1", id, activity, kind == "response")
		return err
	})
	return found, err
}
