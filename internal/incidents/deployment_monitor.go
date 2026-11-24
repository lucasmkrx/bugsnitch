// SPDX-License-Identifier: MPL-2.0
package incidents

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
)

var artifactDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var checkName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,99}$`)

type MonitorConfig struct {
	PullNumber  int64  `json:"pullNumber,omitempty"`
	PullVersion int64  `json:"pullVersion,omitempty"`
	FixCheck    string `json:"fixCheck,omitempty"`
	FixApp      int64  `json:"fixVerifierAppId,omitempty"`
	Environment string `json:"environment"`
	Candidate   string `json:"candidate"`
	Check       string `json:"check"`
	Deployer    int64  `json:"deployerId"`
	Verifier    int64  `json:"verifierAppId"`
	Window      int    `json:"windowSeconds"`
	Minimum     int    `json:"minimumExposures"`
	Version     int64  `json:"version"`
}

type MonitorRegistration struct {
	ID    string `json:"monitorId"`
	Token string `json:"gateToken"`
}

// The owner selects the original behavior probe and the actors allowed to attest it.
func (s Store) ConfigureMonitor(ctx context.Context, w, p, id string, conf MonitorConfig) (MonitorRegistration, error) {
	var result MonitorRegistration
	if label(conf.Environment) == "" || !revision.MatchString(conf.Candidate) || !checkName.MatchString(conf.Check) ||
		conf.Deployer < 1 || conf.Verifier < 1 || conf.Window < 0 || conf.Window > 90*86400 || conf.Minimum < 0 || conf.Minimum > 1_000_000_000 || conf.Version < 1 {
		return result, ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var installation, repository int64
	err = tx.QueryRowContext(ctx, `SELECT m.installation_id,m.repository_id FROM github_mappings m JOIN projects p
        ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id) WHERE m.workspace_id=$1 AND m.project_id=$2 AND m.active
        FOR NO KEY UPDATE OF p FOR SHARE OF m`, w, p).Scan(&installation, &repository)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrUnmapped
	}
	if err != nil {
		return result, err
	}
	var version int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM incidents WHERE workspace_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, w, p, id).Scan(&version); err != nil {
		return result, err
	}
	if version != conf.Version {
		return result, ErrConflict
	}
	var pullCriteria, issueDigest string
	if conf.PullNumber != 0 || conf.PullVersion != 0 || conf.FixCheck != "" || conf.FixApp != 0 {
		if conf.PullNumber < 1 || conf.PullVersion < 1 || !checkName.MatchString(conf.FixCheck) || conf.FixApp < 1 {
			return result, ErrInvalid
		}
		err = tx.QueryRowContext(ctx, `SELECT o.snapshot->>'criteria',i.requirements_digest FROM pull_observations o JOIN incident_issues i
            ON (i.workspace_id,i.project_id,i.number)=(o.workspace_id,o.project_id,o.issue_number)
            WHERE o.workspace_id=$1 AND o.project_id=$2 AND i.incident_id=$3 AND i.status='linked' AND i.installation_id=$4 AND i.repository_id=$5
              AND i.requirements_digest IS NOT NULL AND o.version=$6 AND o.number=$7 AND o.snapshot->>'state'='MERGED' AND o.snapshot->>'mergedSha'=$8
              AND o.snapshot->>'criteria' IS NOT NULL FOR SHARE OF o,i`, w, p, id, installation, repository, conf.PullVersion, conf.PullNumber, conf.Candidate).Scan(&pullCriteria, &issueDigest)
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrConflict
		}
		if err != nil {
			return result, err
		}
		if !artifactDigest.MatchString(pullCriteria) || !artifactDigest.MatchString(issueDigest) {
			return result, ErrConflict
		}
	} else {
		fix, err := loadReceipt(ctx, tx, w, p, id, "fix", "", "")
		if err != nil {
			return result, err
		}
		if fix == nil || fix.Candidate != conf.Candidate {
			return result, ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE deployment_monitors SET active=false WHERE incident_id=$1 AND environment=$2 AND active`, id, conf.Environment)
	if err != nil {
		return result, err
	}
	var key [32]byte
	if _, err = rand.Read(key[:]); err != nil {
		return result, err
	}
	result = MonitorRegistration{newID(), hex.EncodeToString(key[:])}
	_, err = tx.ExecContext(ctx, `INSERT INTO deployment_monitors(id,workspace_id,project_id,incident_id,environment,candidate,deployer_id,verifier_app_id,
        check_name,monitor_seconds,minimum_exposures,installation_id,repository_id,gate_hash,pull_number,pull_version,pull_criteria,issue_digest,fix_check,fix_app_id)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,nullif($15::bigint,0),nullif($16::bigint,0),nullif($17,''),nullif($18,''),nullif($19,''),nullif($20::bigint,0))`,
		result.ID, w, p, id, conf.Environment, conf.Candidate, conf.Deployer, conf.Verifier, conf.Check, conf.Window, conf.Minimum, installation, repository, digest(result.Token), conf.PullNumber, conf.PullVersion, pullCriteria, issueDigest, conf.FixCheck, conf.FixApp)
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE incidents SET version=version+1 WHERE id=$1", id)
	}
	if err == nil {
		err = event(ctx, tx, w, p, "deployment.monitor_configured", result.ID)
	}
	if err != nil {
		return MonitorRegistration{}, err
	}
	return result, tx.Commit()
}

// GitHubRelease is the projection from the signature-verified public webhook.
// Build is the immutable artifact digest reported by the publisher or measured by the verifier.
type GitHubRelease struct {
	Delivery                                            string
	Installation, Repository                            int64
	Kind, Candidate, Environment, Build, Check, Monitor string
	Deployment, Status, Deployer, Creator, Run, App     int64
	At, Started                                         time.Time
	Positive, Inconclusive                              bool
	Coverage                                            *bool
	Exposures                                           *int64
}

func deploymentKey(id, status int64) string { return fmt.Sprintf("gh-deploy-%d-%d", id, status) }

func (s Store) GitHubRelease(ctx context.Context, sig GitHubRelease) error {
	if sig.Installation < 1 || sig.Repository < 1 || !revision.MatchString(sig.Candidate) || sig.Kind != "fix" && (sig.Deployment < 1 || sig.Status < 1) || sig.At.IsZero() || sig.At.After(time.Now().UTC().Add(time.Second)) {
		return ErrInvalid
	}
	switch sig.Kind {
	case "fix":
		if sig.Monitor == "" || sig.Run < 1 || sig.App < 1 {
			return ErrInvalid
		}
	case "deployment":
		if label(sig.Environment) == "" || sig.Deployer < 1 || sig.Creator < 1 {
			return ErrInvalid
		}
		if !artifactDigest.MatchString(sig.Build) {
			sig.Build = "unknown-artifact"
		}
	case "validation":
		if sig.Monitor == "" || sig.Run < 1 || sig.App < 1 || !artifactDigest.MatchString(sig.Build) || sig.Started.IsZero() ||
			sig.Started.After(sig.At) || sig.Exposures != nil && *sig.Exposures < 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var w, p string
	err = tx.QueryRowContext(ctx, `SELECT p.workspace_id,p.id FROM projects p JOIN github_mappings m ON (m.workspace_id,m.project_id)=(p.workspace_id,p.id)
        WHERE m.installation_id=$1 AND m.repository_id=$2 AND m.active FOR NO KEY UPDATE OF p FOR SHARE OF m`, sig.Installation, sig.Repository).Scan(&w, &p)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnmapped
	}
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO github_deliveries(workspace_id,project_id,delivery) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, w, p, sig.Delivery)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,incident_id,candidate,environment,monitor_seconds,minimum_exposures FROM deployment_monitors
        WHERE active AND installation_id=$1 AND repository_id=$2 AND date_trunc('second',created_at)<=$3 AND
        (($4='deployment' AND environment=$5 AND deployer_id=$6 AND deployer_id=$7) OR
         ($4='validation' AND id=$8 AND candidate=$9 AND verifier_app_id=$10 AND check_name=$11) OR
         ($4='fix' AND id=$8 AND candidate=$9 AND fix_app_id=$10 AND fix_check=$11 AND id IN(SELECT id FROM current_monitor_pull_subjects))) ORDER BY incident_id`,
		sig.Installation, sig.Repository, sig.At, sig.Kind, sig.Environment, sig.Deployer, sig.Creator, sig.Monitor, sig.Candidate, sig.App, sig.Check)
	if err != nil {
		return err
	}
	type watch struct {
		id, incident, candidate, env string
		window, minimum              int
	}
	var monitors []watch
	for rows.Next() {
		var m watch
		if err = rows.Scan(&m.id, &m.incident, &m.candidate, &m.env, &m.window, &m.minimum); err != nil {
			rows.Close()
			return err
		}
		monitors = append(monitors, m)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	key := deploymentKey(sig.Deployment, sig.Status)
	for _, m := range monitors {
		if sig.Kind == "fix" {
			var accepted bool
			err = tx.QueryRowContext(ctx, `INSERT INTO deployment_fix_checks AS f(monitor_id,run_id,completed_at,positive) VALUES($1,$2,$3,$4)
				ON CONFLICT (monitor_id,run_id) DO UPDATE SET disputed=f.disputed OR (f.completed_at,f.positive) IS DISTINCT FROM (excluded.completed_at,excluded.positive),
                    completed_at=greatest(f.completed_at,excluded.completed_at) RETURNING positive AND NOT disputed`, m.id, sig.Run, sig.At, sig.Positive).Scan(&accepted)
			if err != nil {
				return err
			}
			if !accepted {
				continue
			}
			r := Receipt{CommandID: fmt.Sprintf("gh-fix-%d", sig.Run), Kind: "fix", Candidate: sig.Candidate, At: sig.At, Evidence: fmt.Sprintf("Approved regression on the inspected merged revision, GitHub check run %d", sig.Run)}
			if err = recordReceipt(ctx, tx, w, p, m.incident, r, "GitHub integrated fix check", m.id, sig.Run); err != nil {
				return err
			}
			continue
		}
		if sig.Kind == "deployment" {
			// Keep the latest status of this deployment even if it is delivered before
			// its original success. A repeated status ID cannot restore a revoked success.
			var succeeded bool
			err = tx.QueryRowContext(ctx, `INSERT INTO deployment_activation_states AS a(monitor_id,deployment_id,status_id,succeeded,effective_at)
               VALUES($1,$2,$3,$4,$5) ON CONFLICT(monitor_id,deployment_id,status_id) DO UPDATE SET
               succeeded=a.succeeded AND excluded.succeeded AND a.effective_at=excluded.effective_at,
               effective_at=greatest(a.effective_at,excluded.effective_at) RETURNING succeeded`, m.id, sig.Deployment, sig.Status, sig.Positive, sig.At).Scan(&succeeded)
			if err != nil {
				return err
			}
			if !succeeded {
				continue
			}
			r := Receipt{CommandID: key, Kind: "deployment", Candidate: sig.Candidate, Environment: m.env, Build: sig.Build,
				At: sig.At, WindowSeconds: m.window, MinimumExposures: m.minimum, Evidence: fmt.Sprintf("GitHub deployment %d, successful status %d", sig.Deployment, sig.Status)}
			if err = recordReceipt(ctx, tx, w, p, m.incident, r, "GitHub deployment", m.id, sig.Status); err != nil {
				return err
			}
		} else {
			// Retain the original deployment binding. A revised terminal result cannot certify a rollout.
			_, err = tx.ExecContext(ctx, `INSERT INTO deployment_monitor_checks AS c(monitor_id,run_id,deployment_key,build,started_at,completed_at,positive,coverage,exposures,inconclusive,confirmed_failure)
                VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOT $7 AND NOT $10) ON CONFLICT (monitor_id,run_id) DO UPDATE SET
                   disputed=c.disputed OR (c.deployment_key,c.build,c.started_at,c.completed_at,c.positive,c.coverage,c.exposures,c.inconclusive)
                     IS DISTINCT FROM (excluded.deployment_key,excluded.build,excluded.started_at,excluded.completed_at,excluded.positive,excluded.coverage,excluded.exposures,excluded.inconclusive),
                   confirmed_failure=c.confirmed_failure OR (excluded.confirmed_failure AND (c.build,c.deployment_key,c.started_at)=(excluded.build,excluded.deployment_key,excluded.started_at)),
                   completed_at=greatest(c.completed_at,excluded.completed_at)`,
				m.id, sig.Run, key, sig.Build, sig.Started, sig.At, sig.Positive, sig.Coverage, sig.Exposures, sig.Inconclusive)
			if err != nil {
				return err
			}
		}
		if err = applyCompletedChecks(ctx, tx, w, p, m.incident, m.id, m.env, m.candidate, key); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "SELECT record_deployment_holds($1,$2,$3)", w, p, m.env); err != nil {
			return err
		}
		// A publisher may revise a previously green terminal check. Keep the contradictory receipt
		// for audit and hold only a confirmed failure on the current activation.
		if _, err = tx.ExecContext(ctx, `INSERT INTO deployment_holds(monitor_id,reason,evidence_key)
            SELECT c.monitor_id,'behavior check failed','gh-check-'||c.run_id FROM deployment_monitor_checks c
            JOIN LATERAL (SELECT command_key,build,monitor_id,effective_at FROM incident_evidence WHERE incident_id=$2 AND kind='deployment' AND environment=$3
              ORDER BY effective_at DESC,provider_order DESC,received_at DESC,command_key DESC LIMIT 1) d ON (d.monitor_id,d.command_key,d.build)=(c.monitor_id,c.deployment_key,c.build)
            WHERE c.monitor_id=$1 AND c.confirmed_failure AND c.disputed AND c.started_at>=d.effective_at ON CONFLICT DO NOTHING`, m.id, m.incident, m.env); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func applyCompletedChecks(ctx context.Context, tx *sql.Tx, w, p, id, monitor, env, candidate, deploy string) error {
	rows, err := tx.QueryContext(ctx, `SELECT c.run_id,c.build,c.completed_at,c.positive,c.coverage,c.exposures FROM deployment_monitor_checks c
        JOIN incident_evidence d ON d.monitor_id=c.monitor_id AND d.command_key=c.deployment_key
        WHERE c.monitor_id=$1 AND c.deployment_key=$2 AND d.candidate=$3 AND c.build=d.build AND c.started_at>=d.effective_at AND NOT c.applied AND NOT c.inconclusive AND NOT c.disputed
        ORDER BY c.completed_at,c.run_id`, monitor, deploy, candidate)
	if err != nil {
		return err
	}
	type check struct {
		id int64
		r  Receipt
	}
	var checks []check
	for rows.Next() {
		var c check
		if err = rows.Scan(&c.id, &c.r.Build, &c.r.At, &c.r.Positive, &c.r.Coverage, &c.r.Exposures); err != nil {
			rows.Close()
			return err
		}
		checks = append(checks, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, c := range checks {
		r := c.r
		r.Kind = "validation"
		r.Environment = env
		r.Candidate = candidate
		r.DeploymentID = deploy
		r.CommandID = fmt.Sprintf("gh-check-%d", c.id)
		r.Evidence = fmt.Sprintf("GitHub behavior check run %d", c.id)
		err = recordReceipt(ctx, tx, w, p, id, r, "GitHub check run", monitor, c.id)
		// A different deployment became effective before this check finished.
		if err != nil && !errors.Is(err, ErrConflict) {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE deployment_monitor_checks SET applied=true WHERE monitor_id=$1 AND run_id=$2", monitor, c.id); err != nil {
			return err
		}
	}
	return nil
}

type GateSubject struct {
	Monitor, TokenHash, Candidate, Build string
	Deployment, Status                   int64
}
type GateResult struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
	Hold    int64  `json:"holdId,omitempty"`
}

// The result covers a specific deployment. CI must check it immediately before promotion.
func (s Store) DeploymentGate(ctx context.Context, subject GateSubject) (GateResult, error) {
	var out GateResult
	if !artifactDigest.MatchString(subject.TokenHash) || !artifactDigest.MatchString(subject.Build) || !revision.MatchString(subject.Candidate) || subject.Deployment < 1 || subject.Status < 1 {
		return out, ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var w, p, id, env, fix string
	var enabled, readyFix, integrated bool
	err = tx.QueryRowContext(ctx, `SELECT m.workspace_id,m.project_id,m.incident_id,m.environment,m.candidate,g.active AND NOT p.paused AND NOT ws.stopped,m.pull_number IS NOT NULL,
        (m.pull_number IS NULL OR (m.id IN(SELECT id FROM current_monitor_pull_subjects) AND EXISTS(
           SELECT FROM incident_evidence f WHERE f.monitor_id=m.id AND f.kind='fix' AND f.candidate=m.candidate)))
        FROM deployment_monitors m JOIN github_mappings g ON (g.workspace_id,g.project_id,g.repository_id,g.installation_id)=(m.workspace_id,m.project_id,m.repository_id,m.installation_id)
        JOIN projects p ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id) JOIN workspaces ws ON ws.id=m.workspace_id WHERE m.id=$1 AND m.gate_hash=$2 AND m.active`, subject.Monitor, subject.TokenHash).Scan(&w, &p, &id, &env, &fix, &enabled, &integrated, &readyFix)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrUnmapped
	}
	if err != nil {
		return out, err
	}
	if !readyFix {
		return GateResult{Reason: "Awaiting integrated fix check on the current issue and merged revision"}, nil
	}
	if !enabled {
		return GateResult{Reason: "Workspace stopped, project paused, or repository disconnected"}, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT h.id,h.reason FROM deployment_holds h JOIN deployment_monitors m ON m.id=h.monitor_id
        WHERE m.workspace_id=$1 AND m.project_id=$2 AND m.environment=$3 AND h.cleared_at IS NULL ORDER BY h.id LIMIT 1`, w, p, env).Scan(&out.Hold, &out.Reason)
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	v, err := readVerdict(ctx, tx, w, p, id, env)
	if err != nil {
		return out, err
	}
	if v.Status != "Verified in environment" {
		return GateResult{Reason: v.Status}, nil
	}
	if integrated && v.Fix.MonitorID != subject.Monitor || v.Fix.Candidate != fix || fix != subject.Candidate || v.Deployment.Build != subject.Build || v.Deployment.CommandID != deploymentKey(subject.Deployment, subject.Status) ||
		v.Deployment.MonitorID != subject.Monitor || v.Validation.MonitorID != subject.Monitor {
		return GateResult{Reason: "Awaiting matching provider evidence"}, nil
	}
	return GateResult{Allowed: true, Reason: v.Status}, tx.Commit()
}

func (s Store) ClearDeploymentHold(ctx context.Context, w, p, id string, hold int64) error {
	if hold < 1 {
		return ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE workspace_id=$1 AND id=$2 FOR NO KEY UPDATE`, w, p).Scan(&project); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE deployment_holds h SET cleared_at=clock_timestamp() FROM deployment_monitors m WHERE m.id=h.monitor_id
        AND m.workspace_id=$1 AND m.project_id=$2 AND m.incident_id=$3 AND h.id=$4 AND h.cleared_at IS NULL`, w, p, id, hold)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: "Owner acknowledged deployment hold", EvidenceRef: fmt.Sprint(hold)})
	if err = event(ctx, tx, w, p, "deployment.hold_cleared", id); err != nil {
		return err
	}
	return tx.Commit()
}

type MonitorView struct {
	ID        string `json:"id"`
	Check     string `json:"check"`
	Candidate string `json:"candidate"`
}
type DeploymentHold struct {
	ID       int64     `json:"id"`
	Reason   string    `json:"reason"`
	Evidence string    `json:"evidence"`
	At       time.Time `json:"at"`
}

func readMonitor(ctx context.Context, tx *sql.Tx, w, p, id, env string, v *Verdict) error {
	var m MonitorView
	err := tx.QueryRowContext(ctx, `SELECT id,check_name,candidate FROM deployment_monitors WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3 AND environment=$4 AND active`, w, p, id, env).Scan(&m.ID, &m.Check, &m.Candidate)
	if err == nil {
		v.Monitor = &m
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT h.id,h.reason,h.evidence_key,h.created_at FROM deployment_holds h JOIN deployment_monitors m ON m.id=h.monitor_id
        WHERE m.workspace_id=$1 AND m.project_id=$2 AND m.incident_id=$3 AND m.environment=$4 AND h.cleared_at IS NULL ORDER BY h.id LIMIT 20`, w, p, id, env)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hold DeploymentHold
		if err = rows.Scan(&hold.ID, &hold.Reason, &hold.Evidence, &hold.At); err != nil {
			return err
		}
		v.Holds = append(v.Holds, hold)
	}
	return rows.Err()
}

func (s Store) DisableMonitor(ctx context.Context, w, p, id, monitor string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Match the same project lock used by signed deliveries and reconfiguration.
	var key string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE workspace_id=$1 AND id=$2 FOR NO KEY UPDATE`, w, p).Scan(&key); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE deployment_monitors SET active=false WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3 AND id=$4 AND active`, w, p, id, monitor)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE incidents SET version=version+1 WHERE id=$1", id); err != nil {
		return err
	}
	if err = event(ctx, tx, w, p, "deployment.monitor_disabled", monitor); err != nil {
		return err
	}
	return tx.Commit()
}
