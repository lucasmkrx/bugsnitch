// Package integration records read-only snapshots and evidence for identified pull requests.
package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"regexp"
	"strconv"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/transport/views"
)

var (
	ErrInvalid     = errors.New("invalid pull request inspection input")
	ErrConflict    = errors.New("pull request evidence changed; refresh the inspection")
	ErrUnavailable = errors.New("GitHub App inspection unavailable; check installation, repository and read permissions")
	digest         = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Store struct {
	DB  *sql.DB
	App *github.App
}
type Evidence struct {
	Version   int64  `json:"version"`
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
}
type SignOff struct {
	Version int64  `json:"version"`
	Head    string `json:"head"`
	Base    string `json:"base"`
	Target  string `json:"target"`
}
type SignOffReceipt struct {
	At time.Time `json:"at"`
}
type Pull struct {
	github.PullObservation
	ProjectID         string            `json:"projectId"`
	Number            int64             `json:"number"`
	IssueNumber       int64             `json:"issueNumber"`
	Version           int64             `json:"version"`
	PolicyVersion     int64             `json:"policyVersion"`
	PlanVersion       int64             `json:"planVersion"`
	ObservedAt        time.Time         `json:"observedAt"`
	Blockers          []string          `json:"blockers"`
	AutoMerge         bool              `json:"autoMerge"`
	Validation        string            `json:"validation,omitempty"`
	IndependentReview string            `json:"independentReview,omitempty"`
	Handovers         []HandoverReceipt `json:"handovers,omitempty"`
	Watching          bool              `json:"watching,omitempty"`
	QueueDelivery     json.RawMessage   `json:"queueDelivery,omitempty"`
	SignOff           *SignOffReceipt   `json:"signOff,omitempty"`
}

type scanner interface{ Scan(...any) error }

func scan(src scanner) (Pull, error) {
	var p Pull
	var b []byte
	if err := src.Scan(&b); err != nil {
		return p, err
	}
	err := json.Unmarshal(b, &p)
	return p, err
}

func (s Store) List(ctx context.Context, w string) ([]Pull, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT t.document FROM ("+views.Pulls+" ORDER BY o.observed_at DESC,o.project_id,o.number LIMIT 500) t", w)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pull{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s Store) Refresh(ctx context.Context, w, p string, n, issue, version int64) (Pull, error) {
	var empty Pull
	if n < 1 || issue < 1 || n == issue || version < 0 {
		return empty, ErrInvalid
	}
	if s.App == nil {
		return empty, ErrUnavailable
	}
	var installation, repository int64
	var name string
	const binding = `SELECT m.installation_id,m.repository_id,p.repository FROM github_mappings m JOIN projects p ON (m.workspace_id,m.project_id)=(p.workspace_id,p.id) WHERE m.workspace_id=$1 AND m.project_id=$2 AND m.active`
	err := s.DB.QueryRowContext(ctx, binding, w, p).Scan(&installation, &repository, &name)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	observation, err := s.App.InspectPull(ctx, installation, repository, name, n, issue)
	if err != nil {
		return empty, ErrUnavailable
	}
	observation.Title = incidents.Redact(observation.Title)
	if text := []rune(observation.Title); len(text) > 240 {
		observation.Title = string(text[:240])
	}
	if len(observation.Target) > 256 || incidents.Redact(observation.Target) != observation.Target {
		return empty, ErrInvalid
	}
	return s.save(ctx, w, p, n, issue, version, installation, repository, name, observation)
}

func (s Store) save(ctx context.Context, w, p string, n, issue, version, installation, repository int64, name string, obs github.PullObservation) (Pull, error) {
	var empty Pull
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	// Lock the project before checking the snapshot version so two first observations cannot both win.
	var policy, plan int64
	err = tx.QueryRowContext(ctx, `SELECT p.policy_version,coalesce((SELECT max(version) FROM plan_versions WHERE workspace_id=$1 AND project_id=$2 AND approved_at IS NOT NULL),0)
	FROM projects p JOIN github_mappings m ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id)
	WHERE p.workspace_id=$1 AND p.id=$2 AND p.repository=$3 AND m.installation_id=$4 AND m.repository_id=$5 AND m.active FOR UPDATE OF p FOR SHARE OF m`, w, p, name, installation, repository).Scan(&policy, &plan)
	if err != nil {
		return empty, err
	}
	b, _ := json.Marshal(obs)
	var cur, curIssue int64
	var unchanged bool
	err = tx.QueryRowContext(ctx, `SELECT version,issue_number,snapshot=$4::jsonb AND policy_version=$5 AND plan_version=$6 FROM pull_observations WHERE workspace_id=$1 AND project_id=$2 AND number=$3 FOR UPDATE`, w, p, n, string(b), policy, plan).Scan(&cur, &curIssue, &unchanged)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if cur != version || (cur != 0 && curIssue != issue) {
		return empty, ErrConflict
	}
	if !unchanged {
		cur++
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(workspace_id,project_id,number) DO UPDATE SET snapshot=excluded.snapshot,version=excluded.version,policy_version=excluded.policy_version,plan_version=excluded.plan_version,observed_at=now()`, w, p, n, issue, cur, string(b), policy, plan)
	if err != nil {
		return empty, err
	}
	if !unchanged {
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Repository observation changed", PolicyVersion: policy, SubjectVersion: cur, EvidenceRef: obs.Head})
		err = audit.Record(ctx, tx, w, p, "pull.inspected", strconv.FormatInt(n, 10))
		if err != nil {
			return empty, err
		}
	}
	result, err := scan(tx.QueryRowContext(ctx, "SELECT t.document FROM ("+views.Pulls+" AND o.project_id=$2 AND o.number=$3) t", w, p, n))
	if err != nil {
		return empty, err
	}
	return result, tx.Commit()
}

func (s Store) Record(ctx context.Context, w, p string, n int64, e Evidence) error {
	if n <= 0 || e.Version <= 0 || !digest.MatchString(e.Reference) || (e.Kind != "combined_validation" && e.Kind != "independent_review") {
		return ErrInvalid
	}
	return s.record(ctx, w, p, n, e, nil)
}

func (s Store) Approve(ctx context.Context, w, p string, n int64, a SignOff) error {
	if n < 1 || a.Version < 1 || !commitSHA.MatchString(a.Head) || !commitSHA.MatchString(a.Base) || !github.ValidBranch(a.Target) {
		return ErrInvalid
	}
	subject, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return s.record(ctx, w, p, n, Evidence{a.Version, "sign_off", fmt.Sprintf("%x", sha256.Sum256(subject))}, &a)
}

func (a SignOff) ready(p Pull) bool {
	return p.Version == a.Version && p.State == "OPEN" && p.IssueState == "OPEN" &&
		p.Head == a.Head && p.Base == a.Base && p.Target == a.Target && !p.Draft &&
		p.Checks == "SUCCESS" && p.CheckCount > 0 && p.Review == "APPROVED" &&
		p.Validation != "" && p.IndependentReview != ""
}

func (s Store) record(ctx context.Context, w, p string, n int64, e Evidence, approval *SignOff) error {
	var issue int64
	if err := s.DB.QueryRowContext(ctx, `SELECT issue_number FROM pull_observations WHERE workspace_id=$1 AND project_id=$2 AND number=$3`, w, p, n).Scan(&issue); err != nil {
		return err
	}
	current, err := s.Refresh(ctx, w, p, n, issue, e.Version)
	if err != nil {
		return err
	}
	if current.Version != e.Version || current.State != "OPEN" || current.IssueState != "OPEN" || current.Base == "" {
		return ErrConflict
	}
	if approval != nil && !approval.ready(current) {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fresh bool
	var stopped bool
	if err = tx.QueryRowContext(ctx, "SELECT stopped FROM workspaces WHERE id=$1 FOR SHARE", w).Scan(&stopped); err != nil {
		return err
	}
	if stopped {
		return ErrConflict
	}
	err = tx.QueryRowContext(ctx, `SELECT o.version=$4 AND m.active AND NOT p.paused AND p.policy_version=o.policy_version AND o.plan_version=coalesce((SELECT max(version) FROM plan_versions WHERE workspace_id=$1 AND project_id=$2 AND approved_at IS NOT NULL),0)
	FROM pull_observations o JOIN projects p ON (p.workspace_id,p.id)=(o.workspace_id,o.project_id)
	JOIN github_mappings m ON (m.workspace_id,m.project_id)=(p.workspace_id,p.id)
	WHERE o.workspace_id=$1 AND o.project_id=$2 AND o.number=$3 FOR SHARE OF o,m,p`, w, p, n, e.Version).Scan(&fresh)
	if err != nil {
		return err
	}
	if !fresh {
		return ErrConflict
	}
	role, actor := audit.Actor(ctx)
	source := role + ":" + actor
	res, err := tx.ExecContext(ctx, `INSERT INTO pull_evidence(workspace_id,project_id,number,version,kind,reference,source) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, w, p, n, e.Version, e.Kind, e.Reference, source)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		ctx = audit.WithDetails(ctx, audit.Details{Reason: "Evidence attested against the current pull request snapshot", SubjectVersion: e.Version, PolicyVersion: current.PolicyVersion, EvidenceRef: e.Reference})
		if err = audit.Record(ctx, tx, w, p, "pull."+e.Kind+"_attested", strconv.FormatInt(n, 10)); err != nil {
			return err
		}
	} else {
		var ref string
		if err = tx.QueryRowContext(ctx, `SELECT reference FROM pull_evidence WHERE workspace_id=$1 AND project_id=$2 AND number=$3 AND version=$4 AND kind=$5`, w, p, n, e.Version, e.Kind).Scan(&ref); err != nil {
			return err
		}
		if ref != e.Reference {
			return ErrConflict
		}
	}
	return tx.Commit()
}
