// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"database/sql"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"net/http"
	"strings"
	"time"
)

func (s *Server) shareDiff(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var b protocol.Diff
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := compatible(b.Protocol); err != nil {
		return err
	}
	if len(b.Text) == 0 || len(b.Text) > 24<<10 || hash(b.Text) != b.Digest || incidents.Redact(b.Text) != b.Text {
		return bad("Diff is oversized or fails the digest or redaction check")
	}
	ctx := r.Context()
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var ws, p, device string
		e := tx.QueryRowContext(ctx, `SELECT d.workspace_id,a.project_id,d.id FROM devices d JOIN attempt_reports a ON a.device_id=d.id AND a.workspace_id=d.workspace_id JOIN device_projects m ON m.project_id=a.project_id AND m.device_id=d.id WHERE NOT d.revoked AND d.token_hash=$1 AND a.id=$2 AND a.version=$3 AND a.base_sha=$4 AND a.candidate_sha=$5 AND a.state='completed' FOR SHARE OF d,a`, hash(token), b.AttemptID, b.Version, b.BaseSHA, b.CandidateSHA).Scan(&ws, &p, &device)
		if e == sql.ErrNoRows {
			return denied
		}
		if e != nil {
			return e
		}
		res, e := tx.ExecContext(ctx, `INSERT INTO shared_diffs(workspace_id,project_id,device_id,attempt_id,version,base_sha,candidate_sha,content,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, ws, p, device, b.AttemptID, b.Version, b.BaseSHA, b.CandidateSHA, b.Text, b.Digest)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			var same bool
			if e = tx.QueryRowContext(ctx, "SELECT digest=$1 FROM shared_diffs WHERE workspace_id=$2 AND attempt_id=$3", b.Digest, ws, b.AttemptID).Scan(&same); e != nil {
				return e
			}
			if !same {
				return conflict("A different diff is already shared")
			}
			return nil
		}
		principal := audit.WithActor(ctx, audit.Device, device)
		recorded := audit.WithDetails(principal, audit.Details{Reason: "Owning device explicitly shared a validated diff", SubjectVersion: int64(b.Version), EvidenceRef: b.Digest})
		return event(recorded, tx, ws, p, "artifact.diff_shared", b.AttemptID)
	})
	if err != nil {
		return err
	}
	return send(w, 200, object{"digest": b.Digest})
}

func (s *Server) readDiff(w http.ResponseWriter, r *http.Request, ws string) error {
	var text, base, head, digest string
	var created time.Time
	err := s.DB.QueryRowContext(r.Context(), `SELECT f.content,f.base_sha,f.candidate_sha,f.digest,f.created_at FROM shared_diffs f JOIN attempt_reports a ON a.id=f.attempt_id AND a.workspace_id=f.workspace_id WHERE f.workspace_id=$1 AND f.attempt_id=$2 AND a.version=f.version AND a.candidate_sha=f.candidate_sha`, ws, r.PathValue("id")).Scan(&text, &base, &head, &digest, &created)
	if err != nil {
		return err
	}
	return send(w, 200, object{"text": text, "baseSha": base, "candidateSha": head, "digest": digest, "sharedAt": created})
}
