package incidents

import (
	"database/sql"
	"encoding/json"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"net/http"
	"strconv"
)

func (s Store) GitHubWebhook(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := signedBody(w, r, secret, "X-Hub-Signature-256", "sha256=")
		if !ok {
			return
		}
		delivery := r.Header.Get("X-GitHub-Delivery")
		var p struct {
			Action                   string
			Installation, Repository struct{ ID int64 }
			Issue                    struct{ ID, Number int64 }
			Changes                  map[string]json.RawMessage
			Removed                  []struct{ ID int64 } `json:"repositories_removed"`
		}
		if !uuid.MatchString(delivery) || json.Unmarshal(body, &p) != nil || p.Installation.ID <= 0 {
			http.Error(w, "invalid installation delivery", 422)
			return
		}
		eventName := r.Header.Get("X-GitHub-Event")
		revokeAll := eventName == "installation" && (p.Action == "suspend" || p.Action == "deleted") && p.Repository.ID == 0 && p.Issue.ID == 0
		revokeSome := eventName == "installation_repositories" && p.Action == "removed" && len(p.Removed) > 0
		issueEvent := eventName == "issues" && p.Repository.ID > 0 && p.Issue.ID > 0 && p.Issue.Number > 0
		if !revokeAll && !revokeSome && !issueEvent {
			http.Error(w, "unsupported event", 422)
			return
		}
		ctx := audit.WithActor(r.Context(), audit.Provider, "github/"+strconv.FormatInt(p.Installation.ID, 10))
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Signed GitHub delivery matched the installation mapping", EvidenceRef: delivery})
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			http.Error(w, "intake unavailable", 503)
			return
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(ctx, `SELECT workspace_id,project_id,repository_id FROM github_mappings WHERE installation_id=$1 AND ($2::bigint=0 OR repository_id=$2) AND (active OR NOT $3::boolean) ORDER BY workspace_id,project_id,repository_id FOR UPDATE`, p.Installation.ID, p.Repository.ID, issueEvent)
		if err != nil {
			http.Error(w, "intake unavailable", 503)
			return
		}
		type binding struct {
			w, p string
			repo int64
		}
		var matches []binding
		removed := map[int64]bool{}
		for _, repo := range p.Removed {
			removed[repo.ID] = true
		}
		for rows.Next() {
			var m binding
			if err = rows.Scan(&m.w, &m.p, &m.repo); err != nil {
				break
			}
			if !revokeSome || removed[m.repo] {
				matches = append(matches, m)
			}
		}
		iterationErr := rows.Err()
		rows.Close()
		if err != nil || iterationErr != nil {
			http.Error(w, "intake unavailable", 503)
			return
		}
		if len(matches) == 0 {
			http.Error(w, "unmapped installation or project", 403)
			return
		}
		for _, m := range matches {
			var fresh sql.Result
			fresh, err = tx.ExecContext(ctx, `INSERT INTO github_deliveries(workspace_id,project_id,delivery) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, m.w, m.p, delivery)
			if err != nil {
				break
			}
			n, e := fresh.RowsAffected()
			if e != nil {
				err = e
				break
			}
			if n == 0 {
				continue
			}
			if issueEvent {
				if err = event(ctx, tx, m.w, m.p, "github.issue_change_received", strconv.FormatInt(p.Issue.ID, 10)); err != nil {
					break
				}
				// Headers and issue snapshots are hints. The worker reads the current issue with the installed app.
				_, err = tx.ExecContext(ctx, `UPDATE incident_issues x SET next_run=now() FROM github_mappings m WHERE (x.workspace_id,x.project_id)=(m.workspace_id,m.project_id) AND x.workspace_id=$1 AND x.project_id=$2 AND x.issue_id=$3 AND x.status='linked' AND m.active`, m.w, m.p, p.Issue.ID)
				if err == nil {
					// Lifecycle edges matter even when a close and reopen arrive before the next read.
					// Signed bodies supply a second deduplication key independent of mutable delivery headers.
					substantive := p.Action == "closed" || p.Action == "reopened" || p.Action == "deleted" || p.Action == "transferred" || p.Action == "edited" && (p.Changes["title"] != nil || p.Changes["body"] != nil)
					_, err = tx.ExecContext(ctx, `WITH fresh AS (
					 INSERT INTO planned_issue_signals(workspace_id,project_id,github_id,signal_hash)
					 SELECT workspace_id,project_id,github_id,$4 FROM planned_issue_state WHERE workspace_id=$1 AND project_id=$2 AND github_id=$3
					 ON CONFLICT DO NOTHING RETURNING github_id)
					 UPDATE planned_issue_state SET availability='pending',reason='Awaiting authoritative GitHub read',next_check=now(),
					 generation=generation+CASE WHEN $5 THEN 1 ELSE 0 END WHERE workspace_id=$1 AND project_id=$2 AND github_id IN(SELECT github_id FROM fresh)`, m.w, m.p, p.Issue.ID, digest(string(body)), substantive)
				}
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE github_mappings SET active=false WHERE workspace_id=$1 AND project_id=$2`, m.w, m.p)
				if err == nil {
					_, err = tx.ExecContext(ctx, `UPDATE planned_issue_state SET availability='unknown',reason='Repository mapping disabled',generation=generation+1,next_check=now()
				 WHERE workspace_id=$1 AND project_id=$2 AND reason<>'Repository mapping disabled'`, m.w, m.p)
				}
				if err == nil {
					err = event(ctx, tx, m.w, m.p, "github.mapping_disabled", m.p)
				}
			}
			if err != nil {
				break
			}
		}
		if err != nil || tx.Commit() != nil {
			http.Error(w, "intake unavailable", 503)
			return
		}
		w.WriteHeader(202)
	})
}
