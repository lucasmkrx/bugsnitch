// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func (s *Server) pairing(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		Name       string
		ProjectIDs []string
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 100 || len(b.ProjectIDs) == 0 || len(b.ProjectIDs) > 40 {
		return bad("Choose a device name and one to forty projects")
	}
	code := random(8)
	until := time.Now().UTC().Add(10 * time.Minute)
	ctx := r.Context()
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		seen := map[string]bool{}
		for _, p := range b.ProjectIDs {
			if seen[p] {
				return bad("Duplicate project")
			}
			seen[p] = true
			var one int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE workspace_id=$1 AND id=$2", ws, p).Scan(&one); err != nil {
				return err
			}
		}
		ids, err := json.Marshal(b.ProjectIDs)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO pairings(code_hash,workspace_id,name,project_ids,expires_at) VALUES($1,$2,$3,$4,$5)", hash(code), ws, b.Name, ids, until)
		if err != nil {
			return err
		}
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Short-lived device enrollment authorized for this project"})
		for _, project := range b.ProjectIDs {
			if err = event(ctx, tx, ws, project, "device.enrollment_authorized", project); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return send(w, 201, object{"code": code, "expiresAt": until})
}
func assigned(ctx context.Context, tx *sql.Tx, ws, device string) ([]protocol.Project, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.repository,p.paused FROM projects p JOIN device_projects m ON p.id=m.project_id WHERE m.workspace_id=$1 AND m.device_id=$2 ORDER BY p.id`, ws, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Project{}
	for rows.Next() {
		var p protocol.Project
		if err = rows.Scan(&p.ID, &p.Repository, &p.Paused); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

var credPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var platformPattern = regexp.MustCompile(`^(darwin|linux|windows)/(arm64|amd64)$`)
var shaPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func compatible(v int) error {
	if v != protocol.Version {
		return problem{426, "Incompatible runner protocol. Install a version supporting device protocol 1."}
	}
	return nil
}
func (s *Server) redeem(w http.ResponseWriter, r *http.Request) error {
	if !s.allow(r) {
		return problem{429, "Try again in one minute"}
	}
	var b protocol.Pair
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := compatible(b.Protocol); err != nil {
		return err
	}
	if len(b.Code) != 16 || !credPattern.MatchString(b.Credential) || !platformPattern.MatchString(b.Platform) {
		return bad("Invalid pairing payload")
	}
	var out protocol.Enrollment
	ctx := r.Context()
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var ws, name string
		var ids json.RawMessage
		var device sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT workspace_id,name,project_ids,device_id FROM pairings WHERE code_hash=$1 AND expires_at>now() FOR UPDATE", hash(b.Code)).Scan(&ws, &name, &ids, &device)
		if err == sql.ErrNoRows {
			return denied
		}
		if err != nil {
			return err
		}
		if device.Valid {
			out.DeviceID = device.String
			var ok bool
			err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT FROM devices WHERE id=$1 AND workspace_id=$2 AND token_hash=$3 AND NOT revoked)", out.DeviceID, ws, hash(b.Credential)).Scan(&ok)
			if err != nil {
				return err
			}
			if !ok {
				return denied
			}
		} else {
			out.DeviceID = random(16)
			if _, err = tx.ExecContext(ctx, "INSERT INTO devices(id,workspace_id,name,platform,token_hash) VALUES($1,$2,$3,$4,$5)", out.DeviceID, ws, name, b.Platform, hash(b.Credential)); err != nil {
				return err
			}
			var projectIDs []string
			if err = json.Unmarshal(ids, &projectIDs); err != nil {
				return err
			}
			for _, p := range projectIDs {
				if _, err = tx.ExecContext(ctx, "INSERT INTO device_projects(workspace_id,device_id,project_id) VALUES($1,$2,$3)", ws, out.DeviceID, p); err != nil {
					return err
				}
			}
			if _, err = tx.ExecContext(ctx, "UPDATE pairings SET device_id=$2 WHERE code_hash=$1", hash(b.Code), out.DeviceID); err != nil {
				return err
			}
			principal := audit.WithActor(ctx, audit.Device, out.DeviceID)
			recorded := audit.WithDetails(principal, audit.Details{Reason: "Owner-issued pairing code redeemed by this device"})
			if err = event(recorded, tx, ws, "", "device.paired", out.DeviceID); err != nil {
				return err
			}
		}
		out.Workspace = ws
		out.Projects, err = assigned(ctx, tx, ws, out.DeviceID)
		return err
	})
	if err != nil {
		return err
	}
	return send(w, 200, out)
}
func (s *Server) revoke(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct{}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var revoked bool
		err := tx.QueryRowContext(ctx, "SELECT revoked FROM devices WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, id).Scan(&revoked)
		if err != nil || revoked {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE devices SET revoked=true,token_hash=NULL WHERE workspace_id=$1 AND id=$2", ws, id); err != nil {
			return err
		}
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Owner revoked the device credential"})
		return event(ctx, tx, ws, "", "device.revoked", id)
	})
	if err != nil {
		return err
	}
	return send(w, 200, object{"revoked": true})
}
func (s *Server) syncDevice(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var b protocol.Sync
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := compatible(b.Protocol); err != nil {
		return err
	}
	if len(b.Attempts) > 64 || len(b.Acks) > 64 {
		return bad("Limit each sync to 64 reports")
	}
	ctx := r.Context()
	var out protocol.Enrollment
	err := transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var ws string
		err := tx.QueryRowContext(ctx, "UPDATE devices SET last_contact=now() WHERE token_hash=$1 AND NOT revoked RETURNING id,workspace_id", hash(token)).Scan(&out.DeviceID, &ws)
		if err == sql.ErrNoRows {
			return denied
		}
		if err != nil {
			return err
		}
		out.Workspace = ws
		out.Projects, err = assigned(ctx, tx, ws, out.DeviceID)
		if err != nil {
			return err
		}
		for _, a := range b.Attempts {
			if !slices.ContainsFunc(out.Projects, func(p protocol.Project) bool { return p.ID == a.ProjectID }) {
				return denied
			}
			if a.IssueUpdatedAt != "" {
				if _, err := time.Parse(time.RFC3339, a.IssueUpdatedAt); err != nil || len(a.IssueUpdatedAt) > 40 {
					return bad("Invalid canonical issue revision")
				}
			}
			if !keyPattern.MatchString(a.ID) || a.IssueNumber < 1 || a.Version < 1 || !shaPattern.MatchString(a.BaseSHA) || (a.CandidateSHA != "" && !shaPattern.MatchString(a.CandidateSHA)) || !slices.Contains([]string{"plan", "implement", "validate", "review", "wrap_up"}, a.Phase) || !slices.Contains([]string{"running", "waiting", "completed", "failed", "cancelled"}, a.State) {
				return bad("Invalid attempt metadata")
			}
			if err = saveReport(ctx, tx, ws, out.DeviceID, a); err != nil {
				return err
			}
		}
		var stopped bool
		if err = tx.QueryRowContext(ctx, "SELECT stopped FROM workspaces WHERE id=$1", ws).Scan(&stopped); err != nil {
			return err
		}
		if stopped {
			if err = ensureStops(ctx, tx, ws, out.DeviceID); err != nil {
				return err
			}
		}
		out.Commands, err = s.controls(audit.WithActor(ctx, audit.Device, out.DeviceID), tx, ws, out.DeviceID, b.Acks)
		return err
	})
	if err != nil {
		return err
	}
	return send(w, 200, out)
}
func saveReport(ctx context.Context, tx *sql.Tx, ws, device string, a protocol.Attempt) error {
	// A snapshot may skip versions during disconnection. Ownership and issue/base identity are immutable.
	var owns bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT FROM admission WHERE workspace_id=$1 AND attempt_id=$2 AND device_id=$3 AND project_id=$4 AND issue_number=$5 AND base_sha=$6)", ws, a.ID, device, a.ProjectID, a.IssueNumber, a.BaseSHA).Scan(&owns); err != nil {
		return err
	}
	if !owns {
		return denied
	}
	if !protocol.ValidReason(a.ReasonCode) {
		return bad("Invalid attempt reason code")
	}
	if len(a.Evidence) > 40 {
		return bad("Too many evidence records")
	}
	for _, e := range a.Evidence {
		if !slices.Contains([]string{"plan", "implement", "validate", "review", "wrap_up"}, e.Phase) || (e.CandidateSHA != "" && !shaPattern.MatchString(e.CandidateSHA)) || e.ChangedFiles < 0 || e.ImportantFindings < 0 || e.MinorFindings < 0 {
			return bad("Invalid stage evidence")
		}
	}
	if a.Evidence == nil {
		a.Evidence = []protocol.StageEvidence{}
	}
	evidence, _ := json.Marshal(a.Evidence)
	res, err := tx.ExecContext(ctx, `INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version,evidence,issue_updated_at,reason_code) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (workspace_id,id) DO UPDATE SET reason_code=EXCLUDED.reason_code,issue_updated_at=coalesce(nullif(EXCLUDED.issue_updated_at,''),attempt_reports.issue_updated_at),evidence=EXCLUDED.evidence,candidate_sha=EXCLUDED.candidate_sha,phase=EXCLUDED.phase,state=EXCLUDED.state,version=EXCLUDED.version,updated_at=now() WHERE attempt_reports.device_id=EXCLUDED.device_id AND attempt_reports.project_id=EXCLUDED.project_id AND attempt_reports.issue_number=EXCLUDED.issue_number AND attempt_reports.base_sha=EXCLUDED.base_sha AND (attempt_reports.issue_updated_at='' OR EXCLUDED.issue_updated_at='' OR attempt_reports.issue_updated_at=EXCLUDED.issue_updated_at)
	AND ((attempt_reports.version<EXCLUDED.version AND attempt_reports.state NOT IN('completed','cancelled'))
	OR (attempt_reports.issue_updated_at='' AND EXCLUDED.issue_updated_at<>'' AND (attempt_reports.version,attempt_reports.state,attempt_reports.phase,attempt_reports.candidate_sha,attempt_reports.evidence,attempt_reports.reason_code)=(EXCLUDED.version,EXCLUDED.state,EXCLUDED.phase,EXCLUDED.candidate_sha,EXCLUDED.evidence,EXCLUDED.reason_code)))`, a.ID, ws, a.ProjectID, device, a.IssueNumber, a.BaseSHA, a.CandidateSHA, a.Phase, a.State, a.Version, evidence, a.IssueUpdatedAt, a.ReasonCode)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		ref := a.CandidateSHA
		if ref == "" {
			ref = a.BaseSHA
		}
		principal := audit.WithActor(ctx, audit.Device, device)
		recorded := audit.WithDetails(principal, audit.Details{Reason: "Owning device synchronized a journaled attempt", SubjectVersion: int64(a.Version), EvidenceRef: ref})
		return event(recorded, tx, ws, a.ProjectID, "attempt.reported", a.ID)
	}
	var permitted bool
	err = tx.QueryRowContext(ctx, `SELECT device_id=$3 AND project_id=$4 AND issue_number=$5 AND base_sha=$6 AND (version>$7 OR (version=$7 AND candidate_sha=$8 AND phase=$9 AND state=$10 AND evidence=$11 AND reason_code=$13 AND ($12='' OR issue_updated_at=$12))) FROM attempt_reports WHERE workspace_id=$1 AND id=$2`, ws, a.ID, device, a.ProjectID, a.IssueNumber, a.BaseSHA, a.Version, a.CandidateSHA, a.Phase, a.State, evidence, a.IssueUpdatedAt, a.ReasonCode).Scan(&permitted)
	if err != nil {
		return err
	}
	if !permitted {
		return conflict("Attempt identity or version conflicts with the stored report")
	}
	return nil
}
