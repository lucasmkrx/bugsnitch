// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"encoding/json"
	"regexp"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
)

type costThreshold struct {
	Project  string   `json:"projectId"`
	Version  int64    `json:"version"`
	Mode     string   `json:"mode"`
	Limit    *float64 `json:"thresholdUSD"`
	Month    string   `json:"month"`
	Estimate float64  `json:"estimatedUSD"`
	Admitted int64    `json:"admittedTurns"`
	Metered  int64    `json:"meteredTurns"`
	Priced   int64    `json:"pricedTurns"`
	Reached  bool     `json:"reached"`
}

func (s *Server) costThreshold(c *domainCall) (costThreshold, error) {
	out := costThreshold{Project: c.Project}
	err := s.DB.QueryRowContext(c.ctx, `WITH m AS (SELECT date_trunc('month',now() AT TIME ZONE 'UTC')::date AS start)
        SELECT coalesce(p.version,0),coalesce(p.mode,'off'),p.threshold_usd,m.start::text,coalesce(u.estimated_usd,0),
          CASE WHEN $2='' THEN coalesce((SELECT turns FROM monthly_model_usage WHERE workspace_id=$1 AND month=m.start),0)
               ELSE (SELECT count(*) FROM events WHERE workspace_id=$1 AND project_id=$2 AND kind IN ('local_model.started','admission.turn_start') AND created_at>=m.start::timestamp AT TIME ZONE 'UTC') END,
          coalesce(u.metered_turns,0),coalesce(u.priced_turns,0),coalesce(coalesce(u.estimated_usd,0)>=p.threshold_usd,false)
        FROM m LEFT JOIN monthly_cost_policies p ON (p.workspace_id,p.scope)=($1,$2)
        LEFT JOIN monthly_cost_usage u ON (u.workspace_id,u.scope,u.month)=($1,$2,m.start)`, c.Workspace, c.Project).Scan(&out.Version, &out.Mode, &out.Limit, &out.Month, &out.Estimate, &out.Admitted, &out.Metered, &out.Priced, &out.Reached)
	return out, err
}

var thresholdAmount = regexp.MustCompile(`^(0|[1-9][0-9]{0,9})(\.[0-9]{1,6})?$`)

func (s *Server) setCostThreshold(c *domainCall) error {
	var b struct {
		Version   int64
		Mode      string
		Threshold *json.Number `json:"thresholdUSD"`
	}
	if err := c.decode(&b); err != nil {
		return err
	}
	if b.Version < 0 || (b.Mode != "off" && b.Mode != "alert" && b.Mode != "stop") || (b.Mode == "off") != (b.Threshold == nil) {
		return bad("Choose off, alert or stop and supply a USD threshold when enabled")
	}
	var amount any
	if b.Threshold != nil {
		f, err := b.Threshold.Float64()
		if err != nil || !thresholdAmount.MatchString(b.Threshold.String()) || f <= 0 || f > 1e9 {
			return bad("Choose a positive USD amount up to one billion with at most six decimal places")
		}
		amount = b.Threshold.String()
	}
	err := transaction(c.ctx, s.DB, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(c.ctx, "SELECT id FROM workspaces WHERE id=$1 FOR NO KEY UPDATE", c.Workspace).Scan(&id); err != nil {
			return err
		}
		if c.Project != "" {
			if err := tx.QueryRowContext(c.ctx, "SELECT id FROM projects WHERE workspace_id=$1 AND id=$2", c.Workspace, c.Project).Scan(&id); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(c.ctx, `INSERT INTO monthly_cost_policies(workspace_id,scope,version,mode,threshold_usd)
            SELECT $1,$2,$3::bigint+1,$4,$5::numeric WHERE $3=coalesce((SELECT version FROM monthly_cost_policies WHERE workspace_id=$1 AND scope=$2),0)
            ON CONFLICT(workspace_id,scope) DO UPDATE SET version=excluded.version,mode=excluded.mode,threshold_usd=excluded.threshold_usd`,
			c.Workspace, c.Project, b.Version, b.Mode, amount)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return conflict("Monthly USD policy changed. Read usage for its current version.")
		}
		ctx := audit.WithDetails(c.ctx, audit.Details{Reason: "Owner updated the monthly estimated USD threshold", SubjectVersion: b.Version + 1})
		if err = event(ctx, tx, c.Workspace, c.Project, "budget.cost_threshold_updated", c.Workspace); err != nil {
			return err
		}
		_, err = tx.ExecContext(c.ctx, "SELECT notify_monthly_cost_threshold($1,$2)", c.Workspace, c.Project)
		return err
	})
	if err != nil {
		return err
	}
	updated, err := s.costThreshold(c)
	if err != nil {
		return err
	}
	return c.reply(200, updated)
}
