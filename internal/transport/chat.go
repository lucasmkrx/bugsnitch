// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
)

var slackTeam = regexp.MustCompile(`^T[A-Z0-9]{2,31}$`)
var slackUser = regexp.MustCompile(`^[UW][A-Z0-9]{2,31}$`)

type chatIdentity struct{ Provider, Tenant, User string }

func (c chatIdentity) valid() bool {
	if c.Provider == "telegram" {
		n, e := strconv.ParseInt(c.User, 10, 64)
		return c.Tenant == "private" && e == nil && n > 0 && strconv.FormatInt(n, 10) == c.User
	}
	return c.Provider == "slack" && slackTeam.MatchString(c.Tenant) && slackUser.MatchString(c.User)
}
func (s *Server) bindChat(call *domainCall) error {
	var b struct {
		chatIdentity
		Remove bool
	}
	if err := call.decode(&b); err != nil {
		return err
	}
	if !b.valid() {
		return bad("Enter the provider account IDs displayed by Slack or Telegram")
	}
	err := transaction(call.ctx, s.DB, func(tx *sql.Tx) error {
		var role string
		if err := tx.QueryRowContext(call.ctx, "SELECT role FROM workspace_members WHERE workspace_id=$1 AND id=$2 AND revoked_at IS NULL FOR UPDATE", call.Workspace, call.ID).Scan(&role); err != nil {
			return err
		}
		if role != "approver" {
			return bad("Select an active approver")
		}
		if b.Remove {
			_, err := tx.ExecContext(call.ctx, "DELETE FROM member_chat_identities WHERE workspace_id=$1 AND member_id=$2 AND provider=$3 AND tenant=$4 AND user_id=$5", call.Workspace, call.ID, b.Provider, b.Tenant, b.User)
			if err != nil {
				return err
			}
		} else {
			_, err := tx.ExecContext(call.ctx, `INSERT INTO member_chat_identities(workspace_id,member_id,provider,tenant,user_id) VALUES($1,$2,$3,$4,$5)
       ON CONFLICT(workspace_id,member_id,provider) DO UPDATE SET tenant=excluded.tenant,user_id=excluded.user_id`, call.Workspace, call.ID, b.Provider, b.Tenant, b.User)
			if err != nil {
				return err
			}
		}
		return event(call.ctx, tx, call.Workspace, "", "member.chat_changed", call.ID)
	})
	if err != nil {
		return err
	}
	return call.reply(200, object{"linked": !b.Remove})
}
func (s *Server) chatCommand(ctx context.Context, input []byte) (any, error) {
	var b struct {
		chatIdentity
		Text string
	}
	if err := json.Unmarshal(input, &b); err != nil || !b.valid() || len(b.Text) > 200 {
		return nil, bad("Invalid chat command")
	}
	words := strings.Fields(b.Text)
	var message string
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var ws, id string
		var project sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT m.workspace_id,m.id,m.project_id FROM member_chat_identities c JOIN workspace_members m ON (m.workspace_id,m.id)=(c.workspace_id,c.member_id)
      WHERE c.provider=$1 AND c.tenant=$2 AND c.user_id=$3 AND m.role='approver' AND m.revoked_at IS NULL FOR SHARE OF m,c`, b.Provider, b.Tenant, b.User).Scan(&ws, &id, &project)
		if errors.Is(err, sql.ErrNoRows) {
			return denied
		}
		if err != nil {
			return err
		}
		if len(words) == 0 || (len(words) == 1 && words[0] == "list") {
			rows, err := tx.QueryContext(ctx, `SELECT d.id,d.version,p.repository FROM decisions d JOIN projects p ON (p.workspace_id,p.id)=(d.workspace_id,d.project_id)
      WHERE d.workspace_id=$1 AND ($2::text IS NULL OR d.project_id=$2) AND d.answer IS NULL AND d.expires_at>now() AND p.version=d.subject_version ORDER BY d.created_at LIMIT 10`, ws, project)
			if err != nil {
				return err
			}
			defer rows.Close()
			var lines []string
			for rows.Next() {
				var d, repo string
				var v int64
				if err = rows.Scan(&d, &v, &repo); err != nil {
					return err
				}
				lines = append(lines, fmt.Sprintf("%s · resume dispatch? %s %d", repo, d, v))
			}
			if err = rows.Err(); err != nil {
				return err
			}
			message = "No pending dispatch decisions."
			if len(lines) > 0 {
				message = strings.Join(lines, "\n") + "\nUse resume ID VERSION or pause ID VERSION."
			}
			return nil
		}
		if len(words) != 3 || (words[0] != "resume" && words[0] != "pause") || len(words[1]) != 32 {
			return bad("Use list, resume ID VERSION, or pause ID VERSION")
		}
		n, e := strconv.ParseInt(words[2], 10, 64)
		if e != nil || n < 1 {
			return bad("Invalid decision version")
		}
		var p string
		if err = tx.QueryRowContext(ctx, "SELECT project_id FROM decisions WHERE workspace_id=$1 AND id=$2", ws, words[1]).Scan(&p); err != nil {
			return err
		}
		if project.Valid && project.String != p {
			return denied
		}
		answer := "Keep paused"
		if words[0] == "resume" {
			answer = "Resume dispatch"
		}
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{b.Provider, b.Tenant, b.User, words[0], words[1], strconv.FormatInt(n, 10)}, "/"))))
		if err = applyAnswerTx(audit.WithActor(ctx, audit.Approver, id), tx, ws, words[1], decisionAnswer{Answer: answer, RequestID: key, Version: n}); err != nil {
			return err
		}
		message = answer + " recorded."
		return nil
	})
	return message, err
}
