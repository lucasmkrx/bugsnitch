package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var revision = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type Receipt struct {
	CommandID        string    `json:"commandId"`
	Version          int64     `json:"version"`
	Kind             string    `json:"kind"`
	Candidate        string    `json:"candidate"`
	Environment      string    `json:"environment"`
	Build            string    `json:"build"`
	Evidence         string    `json:"evidence"`
	At               time.Time `json:"at"`
	Positive         *bool     `json:"positive,omitempty"`
	Coverage         *bool     `json:"coverage,omitempty"`
	Exposures        *int64    `json:"exposures,omitempty"`
	WindowSeconds    int       `json:"windowSeconds,omitempty"`
	MinimumExposures int       `json:"minimumExposures,omitempty"`
}

func (r Receipt) valid() bool {
	if !requestKey.MatchString(r.CommandID) || r.Version <= 0 || !revision.MatchString(r.Candidate) || len(r.Evidence) > 2048 || r.Evidence == "" || r.At.IsZero() || r.At.After(time.Now().UTC().Add(time.Second)) {
		return false
	}
	if r.WindowSeconds < 0 || r.WindowSeconds > 90*86400 || r.MinimumExposures < 0 || r.Exposures != nil && *r.Exposures < 0 {
		return false
	}
	switch r.Kind {
	case "fix":
		return r.Environment == "" && r.Build == "" && r.Positive == nil && r.Coverage == nil && r.Exposures == nil && r.WindowSeconds == 0 && r.MinimumExposures == 0
	case "deployment":
		return label(r.Environment) != "" && validBuild(r.Build) && r.Positive == nil && r.Coverage == nil && r.Exposures == nil
	case "validation":
		return label(r.Environment) != "" && validBuild(r.Build) && r.Positive != nil && r.WindowSeconds == 0 && r.MinimumExposures == 0
	}
	return false
}
func validBuild(s string) bool { return revision.MatchString(s) || label(s) != "" }
func (s Store) receipt(ctx context.Context, w, p, id string, r Receipt) error {
	r.Evidence = Redact(r.Evidence)
	if !r.valid() {
		return ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM incidents WHERE workspace_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, w, p, id).Scan(&cur); err != nil {
		return err
	}
	raw, _ := json.Marshal(r)
	hash := digest(string(raw))
	var stored string
	err = tx.QueryRowContext(ctx, `SELECT payload_hash FROM incident_evidence WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3 AND command_key=$4`, w, p, id, r.CommandID).Scan(&stored)
	if err == nil {
		if stored == hash {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cur != r.Version {
		return ErrConflict
	}
	if r.Kind == "validation" {
		// Bind to one concrete deployment, including its timestamp. A redeployment of the same
		// artifact requires fresh positive evidence after that deployment.
		var ok bool
		err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT build=$4 AND candidate=$5 FROM incident_evidence WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3 AND environment=$6 AND kind='deployment' AND effective_at<=$7 ORDER BY effective_at DESC,received_at DESC LIMIT 1),false)`, w, p, id, r.Build, r.Candidate, r.Environment, r.At).Scan(&ok)
		if err != nil {
			return err
		}
		if !ok {
			return ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO incident_evidence(workspace_id,project_id,incident_id,command_key,payload_hash,kind,candidate,environment,build,evidence,effective_at,positive,coverage,exposures,monitor_seconds,minimum_exposures)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, w, p, id, r.CommandID, hash, r.Kind, r.Candidate, r.Environment, r.Build, r.Evidence, r.At, r.Positive, r.Coverage, r.Exposures, r.WindowSeconds, r.MinimumExposures)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE incidents SET version=version+1,state=CASE WHEN $2='fix' THEN 'Fix validated' ELSE state END WHERE id=$1`, id, r.Kind); err != nil {
		return err
	}
	if err = event(ctx, tx, w, p, "incident."+r.Kind+"_recorded", id); err != nil {
		return err
	}
	return tx.Commit()
}

type Verdict struct {
	Status      string   `json:"status"`
	Reason      string   `json:"reason"`
	Environment string   `json:"environment"`
	Deployment  *Receipt `json:"deployment,omitempty"`
	Fix         *Receipt `json:"fix,omitempty"`
	Validation  *Receipt `json:"validation,omitempty"`
	Source      string   `json:"receiptSource"`
}

func evaluate(v Verdict, now time.Time, recurrence, unknown bool) Verdict {
	set := func(status, reason string) Verdict { v.Status = status; v.Reason = reason; return v }
	if v.Fix == nil {
		return set("Needs reproduction", "A validated fix revision and check evidence are required.")
	}
	if v.Deployment == nil || v.Deployment.Candidate != v.Fix.Candidate {
		return set("Awaiting deployment", "The identified environment needs a receipt for this fix revision.")
	}
	if recurrence {
		return set("Needs triage", "A qualifying report identifies this deployment and environment.")
	}
	if unknown {
		return set("Verification inconclusive", "A report has incomplete environment or release information.")
	}
	a := v.Validation
	d := v.Deployment
	if a == nil || a.Positive == nil {
		return set("Verification inconclusive", "Positive validation of the affected behavior is required on this build.")
	}
	if !*a.Positive {
		return set("Needs triage", "The latest validation failed on this build.")
	}
	if d.WindowSeconds > 0 || d.MinimumExposures > 0 {
		if a.Coverage == nil || !*a.Coverage {
			return set("Coverage unknown", "Functioning application instrumentation needs a current attestation.")
		}
		end := d.At.Add(time.Duration(d.WindowSeconds) * time.Second)
		if now.Before(end) || a.At.Before(end) {
			return set("Monitoring", "The observation window needs a validation receipt covering its end.")
		}
		if d.MinimumExposures > 0 && (a.Exposures == nil || *a.Exposures < int64(d.MinimumExposures)) {
			return set("Verification inconclusive", "The observation has insufficient documented exposure.")
		}
	}
	return set("Verified in environment", "Positive evidence applies to the current deployment.")
}
func loadReceipt(ctx context.Context, tx *sql.Tx, w, p, id, kind, env string, since time.Time) (*Receipt, error) {
	var r Receipt
	err := tx.QueryRowContext(ctx, `SELECT command_key,kind,candidate,environment,build,evidence,effective_at,positive,coverage,exposures,monitor_seconds,minimum_exposures FROM incident_evidence
	WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3 AND kind=$4 AND environment=$5 AND effective_at>=$6 ORDER BY effective_at DESC,received_at DESC,command_key DESC LIMIT 1`, w, p, id, kind, env, since).Scan(&r.CommandID, &r.Kind, &r.Candidate, &r.Environment, &r.Build, &r.Evidence, &r.At, &r.Positive, &r.Coverage, &r.Exposures, &r.WindowSeconds, &r.MinimumExposures)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}
func (s Store) verdict(ctx context.Context, w, p, id, env string) (Verdict, error) {
	v := Verdict{Environment: env, Source: "authenticated owner"}
	if label(env) == "" {
		return v, ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	var group, source string
	if err = tx.QueryRowContext(ctx, `SELECT group_key,source FROM incidents WHERE workspace_id=$1 AND project_id=$2 AND id=$3`, w, p, id).Scan(&group, &source); err != nil {
		return v, err
	}
	if v.Fix, err = loadReceipt(ctx, tx, w, p, id, "fix", "", time.Time{}); err != nil {
		return v, err
	}
	if v.Deployment, err = loadReceipt(ctx, tx, w, p, id, "deployment", env, time.Time{}); err != nil {
		return v, err
	}
	var recurrence, unknown bool
	if d := v.Deployment; d != nil {
		if v.Validation, err = loadReceipt(ctx, tx, w, p, id, "validation", env, d.At); err != nil {
			return v, err
		}
		if a := v.Validation; a != nil && (a.Candidate != d.Candidate || a.Build != d.Build) {
			v.Validation = nil
		}
		err = tx.QueryRowContext(ctx, `SELECT coalesce(bool_or(environment=$5 AND release=$6),false),
		coalesce(bool_or((environment='' OR release='') AND (environment='' OR environment=$5) AND (release='' OR release=$6)),false)
		FROM incident_inbox WHERE workspace_id=$1 AND project_id=$2 AND source=$3 AND group_key=$4 AND seen_at>=$7`, w, p, source, group, env, d.Build, d.At).Scan(&recurrence, &unknown)
		if err != nil {
			return v, err
		}
	}
	return evaluate(v, time.Now().UTC(), recurrence, unknown), tx.Commit()
}

// VerificationHTTP requires an outer owner-session and CSRF gate. Workspace is supplied by that gate.
func (s Store) VerificationHTTP(w http.ResponseWriter, r *http.Request, workspace, project, incidentID string) {
	var err error
	var result any
	switch r.Method {
	case "GET":
		result, err = s.verdict(r.Context(), workspace, project, incidentID, r.URL.Query().Get("environment"))
	case "POST":
		var receipt Receipt
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&receipt) != nil || d.Decode(new(any)) != io.EOF {
			err = ErrInvalid
			break
		}
		err = s.receipt(r.Context(), workspace, project, incidentID, receipt)
		result = map[string]string{"commandId": receipt.CommandID, "status": "recorded"}
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method unavailable", 405)
		return
	}
	if err != nil {
		code := 503
		switch {
		case errors.Is(err, ErrInvalid):
			code = 400
		case errors.Is(err, ErrConflict):
			code = 409
		case errors.Is(err, sql.ErrNoRows):
			code = 404
		}
		http.Error(w, strings.ToLower(http.StatusText(code)), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(result)
}
