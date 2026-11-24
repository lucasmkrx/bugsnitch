// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"regexp"
)

type TokenRate struct {
	Input  *float64 `json:"inputPerMillionUSD"`
	Output *float64 `json:"outputPerMillionUSD"`
}
type TokenUsage struct {
	Priced   int64    `json:"pricedTurns"`
	Model    string   `json:"model"`
	Turns    int64    `json:"turns"`
	Input    int64    `json:"inputTokens"`
	Output   int64    `json:"outputTokens"`
	Estimate *float64 `json:"estimatedUSD,omitempty"`
}
type MeterReport struct {
	MonthlyThreshold *costThreshold        `json:"monthlyThreshold,omitempty"`
	Days             int                   `json:"days"`
	Admitted         int64                 `json:"admittedTurns"`
	Unmetered        int64                 `json:"unmeteredTurns"`
	Failed           int64                 `json:"turnsOnFailedOrCancelledAttempts"`
	Unpriced         int64                 `json:"unpricedMeteredTurns"`
	Rates            map[string]PriceEntry `json:"workspacePrices"`
	Models           []TokenUsage          `json:"models"`
}

func (s *Server) meterReport(c *domainCall) error {
	var input struct {
		Days  int                  `json:"days"`
		Rates map[string]TokenRate `json:"rates"`
	}
	if err := c.decode(&input); err != nil {
		return err
	}
	if input.Days == 0 {
		input.Days = 30
	}
	if input.Days < 1 || input.Days > 365 || len(input.Rates) > 128 {
		return bad("Choose a period of 1 to 365 days and at most 128 model prices")
	}
	for k, r := range input.Rates {
		if !validRate(k, r) {
			return bad("Enter nonnegative input and output prices per million tokens")
		}
	}
	role, id := audit.Actor(c.ctx)
	rows, err := s.DB.QueryContext(c.ctx, `SELECT coalesce(e.model,''),count(*),coalesce(sum(e.input_tokens),0),coalesce(sum(e.output_tokens),0),
        count(*) FILTER (WHERE a.state IN ('failed','cancelled')),count(e.estimated_usd),sum(e.estimated_usd) FROM priced_model_turns e
        LEFT JOIN attempt_reports a ON e.kind='admission.turn_start' AND (a.workspace_id,a.project_id,a.id)=(e.workspace_id,e.project_id,e.subject_id)
        WHERE e.workspace_id=$1 AND ($2='' OR e.project_id=$2) AND e.kind IN ('admission.turn_start','local_model.started')
          AND e.created_at>=now()-make_interval(days=>$3)
          AND (EXISTS(SELECT FROM workspaces WHERE id=e.workspace_id AND account_id=$5 AND $4='owner')
            OR EXISTS(SELECT FROM workspace_members WHERE workspace_id=e.workspace_id AND id=$5 AND role=$4 AND revoked_at IS NULL AND (project_id IS NULL OR project_id=$2)))
        GROUP BY e.model ORDER BY e.model`, c.Workspace, c.Project, input.Days, role, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := MeterReport{Days: input.Days, Models: []TokenUsage{}}
	for rows.Next() {
		var row TokenUsage
		var failed int64
		if err = rows.Scan(&row.Model, &row.Turns, &row.Input, &row.Output, &failed, &row.Priced, &row.Estimate); err != nil {
			return err
		}
		out.Admitted += row.Turns
		out.Failed += failed
		if row.Model == "" {
			out.Unmetered += row.Turns
			continue
		}
		if rate, ok := input.Rates[row.Model]; ok {
			inputPrice, outputPrice := *rate.Input, *rate.Output
			dollars := (float64(row.Input)*inputPrice + float64(row.Output)*outputPrice) / 1e6
			row.Priced = row.Turns
			row.Estimate = &dollars
		}
		out.Unpriced += row.Turns - row.Priced
		out.Models = append(out.Models, row)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if role == "owner" {
		status, err := s.costThreshold(c)
		if err != nil {
			return err
		}
		out.MonthlyThreshold = &status
	}
	if role == "owner" && c.Project == "" {
		prices, err := s.DB.QueryContext(c.ctx, `SELECT DISTINCT ON (model) model,version,input_per_million_usd,output_per_million_usd FROM model_price_history
            WHERE workspace_id=$1 ORDER BY model,version DESC`, c.Workspace)
		if err != nil {
			return err
		}
		defer prices.Close()
		out.Rates = map[string]PriceEntry{}
		for prices.Next() {
			var model string
			var entry PriceEntry
			if err = prices.Scan(&model, &entry.Version, &entry.Input, &entry.Output); err != nil {
				return err
			}
			out.Rates[model] = entry
		}
		if err = prices.Err(); err != nil {
			return err
		}
	}
	return c.reply(200, out)
}

type PriceEntry struct {
	TokenRate
	Version int64 `json:"version"`
}

var priceModel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,119}$`)

func validRate(model string, r TokenRate) bool {
	return priceModel.MatchString(model) && incidents.Redact(model) == model && r.Input != nil && r.Output != nil && *r.Input >= 0 && *r.Input <= 100000 && *r.Output >= 0 && *r.Output <= 100000
}
func (s *Server) setModelPrice(c *domainCall) error {
	var b struct {
		PriceEntry
		Model  string `json:"model"`
		Remove bool   `json:"remove"`
	}
	if err := c.decode(&b); err != nil {
		return err
	}
	if b.Version < 0 || !priceModel.MatchString(b.Model) || incidents.Redact(b.Model) != b.Model ||
		(!b.Remove && !validRate(b.Model, b.TokenRate)) || (b.Remove && (b.Version == 0 || b.Input != nil || b.Output != nil)) {
		return bad("Check the model, price version and rates")
	}
	err := transaction(c.ctx, s.DB, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(c.ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", c.Workspace).Scan(&id); err != nil {
			return err
		}
		res, err := tx.ExecContext(c.ctx, `INSERT INTO model_price_history(workspace_id,model,version,input_per_million_usd,output_per_million_usd)
            SELECT $1,$2,$3::bigint+1,$4,$5 WHERE $3=(SELECT coalesce(max(version),0) FROM model_price_history WHERE workspace_id=$1 AND model=$2)`, c.Workspace, b.Model, b.Version, b.Input, b.Output)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return conflict("Model price changed. Read usage for the current version.")
		}
		ctx := audit.WithDetails(c.ctx, audit.Details{Reason: "Owner changed future model price", SubjectVersion: b.Version + 1})
		return event(ctx, tx, c.Workspace, "", "usage.price_changed", b.Model)
	})
	if err != nil {
		return err
	}
	return c.reply(200, object{"model": b.Model, "version": b.Version + 1})
}
