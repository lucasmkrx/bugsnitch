package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
)

const receiptOrder = "effective_at DESC,provider_order DESC,received_at DESC,command_key DESC"

var revision = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type Receipt struct {
	providerOrder    int64
	Source           string    `json:"source,omitempty"`
	MonitorID        string    `json:"monitorId,omitempty"`
	CommandID        string    `json:"commandId"`
	DeploymentID     string    `json:"deploymentId,omitempty"`
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
	if r.DeploymentID != "" && (r.Kind != "validation" || !requestKey.MatchString(r.DeploymentID)) {
		return false
	}
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
func (s Store) RecordReceipt(ctx context.Context, w, p, id string, r Receipt) error {
	if r.Source != "" || r.MonitorID != "" {
		return ErrInvalid
	}
	r.Evidence = Redact(r.Evidence)
	if !r.valid() {
		return ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = recordReceipt(ctx, tx, w, p, id, r, "authenticated owner", "", 0); err != nil {
		return err
	}
	return tx.Commit()
}

func recordReceipt(ctx context.Context, tx *sql.Tx, w, p, id string, r Receipt, source, monitor string, order int64) error {
	var cur int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM incidents WHERE workspace_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, w, p, id).Scan(&cur); err != nil {
		return err
	}
	hashInput := r
	if monitor != "" {
		hashInput.Version = 0
		hashInput.MonitorID = monitor
		hashInput.Source = source
	}
	raw, _ := json.Marshal(hashInput)
	hash := digest(string(raw))
	var stored string
	err := tx.QueryRowContext(ctx, `SELECT payload_hash FROM incident_evidence WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3 AND command_key=$4`, w, p, id, r.CommandID).Scan(&stored)
	if err == nil {
		if stored == hash {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if monitor != "" {
		r.Version = cur
	}
	if cur != r.Version {
		return ErrConflict
	}
	var deployment sql.NullString
	if r.Kind == "validation" {
		// The incident lock serializes receipt writes. Resolve the deployment
		// as of the observation, then retain that identity for future verdicts.
		var sameArtifact, repeatedArtifact bool
		err = tx.QueryRowContext(ctx, `SELECT command_key,build=$4 AND candidate=$5,
			count(*) FILTER (WHERE build=$4 AND candidate=$5) OVER () > 1 FROM incident_evidence
			WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3 AND environment=$6 AND kind='deployment' AND effective_at<=$7
			ORDER BY `+receiptOrder+` LIMIT 1`, w, p, id, r.Build, r.Candidate, r.Environment, r.At).Scan(&deployment, &sameArtifact, &repeatedArtifact)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		// A delayed observation from a previous deployment of identical
		// bytes must not verify its successor based on timestamps alone.
		needsID := repeatedArtifact || r.DeploymentID != ""
		if !sameArtifact || needsID && r.DeploymentID != deployment.String {
			return ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO incident_evidence(workspace_id,project_id,incident_id,command_key,payload_hash,kind,candidate,environment,build,evidence,effective_at,positive,coverage,exposures,monitor_seconds,minimum_exposures,deployment_key,source,monitor_id,provider_order)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,NULLIF($19,''),$20)`, w, p, id, r.CommandID, hash, r.Kind, r.Candidate, r.Environment, r.Build, r.Evidence, r.At, r.Positive, r.Coverage, r.Exposures, r.WindowSeconds, r.MinimumExposures, deployment, source, monitor, order)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE incidents SET version=version+1,state=CASE WHEN $2='fix' THEN 'Fix validated' ELSE state END WHERE id=$1`, id, r.Kind); err != nil {
		return err
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: "Version-bound incident evidence accepted", SubjectVersion: r.Version, EvidenceRef: r.Candidate})
	if err = event(ctx, tx, w, p, "incident."+r.Kind+"_recorded", id); err != nil {
		return err
	}
	return nil
}

type Verdict struct {
	Monitor     *MonitorView     `json:"monitor,omitempty"`
	Holds       []DeploymentHold `json:"holds,omitempty"`
	Status      string           `json:"status"`
	Reason      string           `json:"reason"`
	Environment string           `json:"environment"`
	Deployment  *Receipt         `json:"deployment,omitempty"`
	Fix         *Receipt         `json:"fix,omitempty"`
	Validation  *Receipt         `json:"validation,omitempty"`
	Source      string           `json:"receiptSource"`
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
func loadReceipt(ctx context.Context, tx *sql.Tx, w, p, id, kind, env, deployment string) (*Receipt, error) {
	var r Receipt
	err := tx.QueryRowContext(ctx, `SELECT command_key,kind,candidate,environment,build,evidence,effective_at,positive,coverage,exposures,monitor_seconds,minimum_exposures,coalesce(deployment_key,''),source,coalesce(monitor_id,''),provider_order FROM incident_evidence
	WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3 AND kind=$4 AND environment=$5 AND ($6='' OR deployment_key=$6)
	ORDER BY `+receiptOrder+` LIMIT 1`, w, p, id, kind, env, deployment).Scan(&r.CommandID, &r.Kind, &r.Candidate, &r.Environment, &r.Build, &r.Evidence, &r.At, &r.Positive, &r.Coverage, &r.Exposures, &r.WindowSeconds, &r.MinimumExposures, &r.DeploymentID, &r.Source, &r.MonitorID, &r.providerOrder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}
func (s Store) Verdict(ctx context.Context, w, p, id, env string) (Verdict, error) {
	v := Verdict{}
	if label(env) == "" {
		return v, ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	v, err = readVerdict(ctx, tx, w, p, id, env)
	if err != nil {
		return v, err
	}
	if err = readMonitor(ctx, tx, w, p, id, env, &v); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

// VerifiedHandoverDeployments returns the provider receipts for active monitors on
// this target. The caller shares the project lock with signed webhook writers.
func VerifiedHandoverDeployments(ctx context.Context, tx *sql.Tx, w, p string, pull int64, candidate, env, build string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.incident_id,m.id IN(SELECT id FROM current_monitor_pull_subjects)
	    AND g.active AND NOT p.paused AND NOT ws.stopped AND NOT EXISTS (
	        SELECT FROM deployment_holds h JOIN deployment_monitors x ON x.id=h.monitor_id
	        WHERE (x.workspace_id,x.project_id,x.environment)=(m.workspace_id,m.project_id,m.environment) AND h.cleared_at IS NULL)
	    FROM deployment_monitors m JOIN github_mappings g USING(workspace_id,project_id,installation_id,repository_id)
	    JOIN projects p ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id) JOIN workspaces ws ON ws.id=p.workspace_id
	    WHERE m.workspace_id=$1 AND m.project_id=$2 AND m.pull_number=$3 AND m.candidate=$4 AND m.environment=$5 AND m.active`, w, p, pull, candidate, env)
	if err != nil {
		return nil, err
	}
	monitors := map[string]string{}
	for rows.Next() {
		var id, incident string
		var eligible bool
		if err = rows.Scan(&id, &incident, &eligible); err != nil {
			rows.Close()
			return nil, err
		}
		if !eligible {
			rows.Close()
			return nil, ErrConflict
		}
		monitors[id] = incident
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	receipts := map[string]string{}
	for id, incident := range monitors {
		v, err := readVerdict(ctx, tx, w, p, incident, env)
		if err != nil {
			return nil, err
		}
		if v.Status != "Verified in environment" || v.Fix.MonitorID != id || v.Validation.MonitorID != id || v.Deployment.MonitorID != id ||
			v.Deployment.Candidate != candidate || v.Deployment.Build != build {
			return nil, ErrConflict
		}
		receipts[id] = v.Deployment.CommandID
	}
	return receipts, nil
}

func readVerdict(ctx context.Context, tx *sql.Tx, w, p, id, env string) (Verdict, error) {
	v := Verdict{Environment: env, Source: "authenticated owner"}
	var err error
	var group, source string
	if err = tx.QueryRowContext(ctx, `SELECT group_key,source FROM incidents WHERE workspace_id=$1 AND project_id=$2 AND id=$3`, w, p, id).Scan(&group, &source); err != nil {
		return v, err
	}
	if v.Fix, err = loadReceipt(ctx, tx, w, p, id, "fix", "", ""); err != nil {
		return v, err
	}
	if v.Deployment, err = loadReceipt(ctx, tx, w, p, id, "deployment", env, ""); err != nil {
		return v, err
	}
	var recurrence, unknown bool
	if d := v.Deployment; d != nil {
		if v.Validation, err = loadReceipt(ctx, tx, w, p, id, "validation", env, d.CommandID); err != nil {
			return v, err
		}
		err = tx.QueryRowContext(ctx, `SELECT coalesce(bool_or(environment=$5 AND release=$6),false),
		coalesce(bool_or((environment='' OR release='') AND (environment='' OR environment=$5) AND (release='' OR release=$6)),false)
		FROM incident_inbox WHERE workspace_id=$1 AND project_id=$2 AND source=$3 AND group_key=$4 AND seen_at>=$7`, w, p, source, group, env, d.Build, d.At).Scan(&recurrence, &unknown)
		if err != nil {
			return v, err
		}
	}
	var provider, owner bool
	for _, receipt := range []*Receipt{v.Fix, v.Deployment, v.Validation} {
		if receipt == nil {
			continue
		}
		if receipt.MonitorID != "" {
			provider = true
		} else {
			owner = true
		}
	}
	if provider {
		v.Source = "GitHub"
		if owner {
			v.Source = "authenticated owner and GitHub"
		}
	}
	v = evaluate(v, time.Now().UTC(), recurrence, unknown)
	if v.Status != "Verified in environment" {
		return v, nil
	}
	if d := v.Deployment; d.MonitorID != "" {
		var active bool
		err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT succeeded AND status_id=$2 FROM deployment_activation_states
               WHERE monitor_id=$1 AND deployment_id=split_part($3::text,'-',3)::bigint ORDER BY effective_at DESC,status_id DESC LIMIT 1),false)`,
			d.MonitorID, d.providerOrder, d.CommandID).Scan(&active)
		if err != nil {
			return v, err
		}
		if !active {
			v.Status = "Awaiting deployment"
			v.Reason = "GitHub no longer reports this deployment status as successful."
			return v, nil
		}
	}
	// Terminal runs can be updated. A contradictory redelivery or newer inconclusive
	// run supersedes the historical receipt until a new, successful run is measured.
	for _, r := range []*Receipt{v.Fix, v.Validation} {
		if r.MonitorID == "" {
			continue
		}
		var current bool
		if r.Kind == "fix" {
			err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT positive AND NOT disputed AND run_id=$2 FROM deployment_fix_checks
                  WHERE monitor_id=$1 ORDER BY completed_at DESC,run_id DESC LIMIT 1),false)`, r.MonitorID, r.providerOrder).Scan(&current)
		} else {
			err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT positive AND NOT inconclusive AND NOT disputed AND run_id=$2
                  FROM deployment_monitor_checks WHERE monitor_id=$1 AND deployment_key=$3 AND build=$4 AND started_at>=$5
                  ORDER BY completed_at DESC,run_id DESC LIMIT 1),false)`, r.MonitorID, r.providerOrder, r.DeploymentID, r.Build, v.Deployment.At).Scan(&current)
		}
		if err != nil {
			return v, err
		}
		if !current {
			v.Status = "Verification inconclusive"
			v.Reason = "A current successful GitHub check is required after a revised or inconclusive result."
			return v, nil
		}
	}
	return v, nil
}
