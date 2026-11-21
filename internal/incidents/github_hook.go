package incidents

import (
	"database/sql"
	"encoding/json"
	"net/http"
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
		ctx := r.Context()
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			http.Error(w, "intake unavailable", 503)
			return
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(ctx, `SELECT workspace_id,project_id,repository_id FROM github_mappings WHERE installation_id=$1 AND ($2::bigint=0 OR repository_id=$2) AND (active OR NOT $3::boolean) ORDER BY repository_id FOR UPDATE`, p.Installation.ID, p.Repository.ID, issueEvent)
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
				// Headers and issue snapshots are hints. The worker reads the current issue with the installed app.
				_, err = tx.ExecContext(ctx, `UPDATE incident_issues x SET next_run=now() FROM github_mappings m WHERE (x.workspace_id,x.project_id)=(m.workspace_id,m.project_id) AND x.workspace_id=$1 AND x.project_id=$2 AND x.issue_id=$3 AND x.status='linked' AND m.active`, m.w, m.p, p.Issue.ID)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE github_mappings SET active=false WHERE workspace_id=$1 AND project_id=$2`, m.w, m.p)
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
