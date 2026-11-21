// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// Values below are static server-owned SQL. The workspace parameter comes from
// the authenticated owner session. Each queue scan stops after its sentinel.
var diagnosticQueues = []struct{ name, pending string }{
	{"incident_intake", "SELECT accepted_at AS since FROM incident_inbox WHERE workspace_id=$1 AND NOT processed"},
	{"incident_issues", "SELECT approved_at AS since FROM incident_issues WHERE workspace_id=$1 AND status IN ('queued','reconciling')"},
	{"planned_issues", "SELECT created_at AS since FROM planned_issues WHERE workspace_id=$1 AND status IN ('queued','reconciling')"},
	{"draft_prs", "SELECT created_at AS since FROM draft_pr_intents WHERE workspace_id=$1 AND status IN ('queued','reconciling')"},
	{"attempt_controls", "SELECT created_at AS since FROM controls WHERE workspace_id=$1 AND status IN ('requested','received') AND expires_at>now()"},
}

type queueObservation struct {
	Queue           string `json:"queue"`
	Pending         int    `json:"pending"`
	More            bool   `json:"more"`
	OldestPendingMS *int64 `json:"oldestPendingMs"`
}

// Summarize a bounded recent sample, never raw stage payloads or unclassified
// messages. General model permits cover the server-observed reservation cycle.
const diagnosticSamples = `
WITH recent AS MATERIALIZED (
 SELECT state,reason_code,evidence FROM attempt_reports WHERE workspace_id=$1 ORDER BY updated_at DESC,id LIMIT 200
), groups AS (
 SELECT state,nullif(reason_code,'') AS reason,count(*) AS count FROM recent
 WHERE state='failed' OR reason_code<>'' GROUP BY state,reason_code
), stage_rows AS (
 SELECT item->>'phase' AS phase,(item->>'durationMs')::bigint AS ms
 FROM recent CROSS JOIN LATERAL jsonb_array_elements(evidence) AS item
), stages AS (
 SELECT phase,count(*) AS observations,count(ms) AS measured,round(avg(ms))::bigint AS "averageMs",max(ms) AS "maximumMs" FROM stage_rows GROUP BY phase
), last_permits AS (
 SELECT role,(extract(epoch FROM (ended_at-created_at))*1000)::bigint AS ms
 FROM general_model_turns WHERE workspace_id=$1 AND ended_at IS NOT NULL ORDER BY ended_at DESC LIMIT 200
), permits AS (
 SELECT role,count(*) AS measured,round(avg(ms))::bigint AS "averageMs",max(ms) AS "maximumMs" FROM last_permits WHERE ms>=0 GROUP BY role
), rejoins AS (
 SELECT created_at FROM events WHERE workspace_id=$1 AND kind='device.reconnected' AND created_at>=now()-interval '24 hours' ORDER BY created_at DESC LIMIT 1001
), contact AS (
 SELECT min(diagnostics_since) AS since,count(diagnostics_since) AS tracked,count(*) AS total FROM devices WHERE workspace_id=$1
)
SELECT jsonb_build_object(
 'sampledAttempts',(SELECT count(*) FROM recent),
 'attemptReasons',coalesce((SELECT jsonb_agg(groups ORDER BY state,reason) FROM groups),'[]'),
 'stageActivity',coalesce((SELECT jsonb_agg(stages ORDER BY phase) FROM stages),'[]'),
 'generalPermitHold',coalesce((SELECT jsonb_agg(permits ORDER BY role) FROM permits),'[]'),
 'contact',jsonb_build_object('trackingSince',since,'trackedDevices',tracked,'totalDevices',total,
   'observedReconnects24h',CASE WHEN tracked>0 THEN least((SELECT count(*) FROM rejoins),1000) END,
   'more',(SELECT count(*)>1000 FROM rejoins),'lastObservedReconnect24h',(SELECT max(created_at) FROM rejoins)))
FROM contact`

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request, workspace string) error {
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var observed time.Time
	var samples json.RawMessage
	if err = tx.QueryRowContext(ctx, "SELECT now()").Scan(&observed); err != nil {
		return err
	}
	queues := make([]queueObservation, 0, len(diagnosticQueues))
	for _, q := range diagnosticQueues {
		entry := queueObservation{Queue: q.name}
		query := "SELECT least(count(*),1000)::int,count(*)>1000,greatest(0::numeric,extract(epoch FROM (now()-min(since)))*1000)::bigint FROM (" + q.pending + " ORDER BY since LIMIT 1001) pending"
		// PostgreSQL greatest() ignores NULL. An empty queue has no age.
		var age int64
		if err = tx.QueryRowContext(ctx, query, workspace).Scan(&entry.Pending, &entry.More, &age); err != nil {
			return err
		}
		if entry.Pending > 0 {
			entry.OldestPendingMS = &age
		}
		queues = append(queues, entry)
	}
	if err = tx.QueryRowContext(ctx, diagnosticSamples, workspace).Scan(&samples); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return send(w, 200, object{"observedAt": observed, "queues": queues, "observations": samples})
}
