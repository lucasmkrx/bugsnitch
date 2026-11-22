package incidents

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
	"github.com/lucasmkrx/bugsnitch/internal/transport/views"
)

type Store struct{ DB *sql.DB }
type Incident struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	Title           string    `json:"title"`
	Observed        string    `json:"observed"`
	Expected        string    `json:"expected"`
	Steps           string    `json:"steps"`
	Source          string    `json:"source"`
	Status          string    `json:"status"`
	Occurrences     int64     `json:"occurrences"`
	FirstSeen       time.Time `json:"firstSeen"`
	LastSeen        time.Time `json:"lastSeen"`
	Version         int64     `json:"version"`
	IssueNumber     *int64    `json:"issueNumber,omitempty"`
	IssueState      *string   `json:"issueState,omitempty"`
	Promotion       *string   `json:"promotion,omitempty"`
	PromotionReason *string   `json:"promotionReason,omitempty"`
	ProviderGroup   *string   `json:"providerGroup,omitempty"`
	Environment     *string   `json:"environment,omitempty"`
	Release         *string   `json:"release,omitempty"`
	Frames          []Frame   `json:"frames,omitempty"`
}

func (s Store) List(ctx context.Context, workspace string) ([]Incident, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, "SELECT coalesce(json_agg(i.document),'[]') FROM ("+views.Incidents+") i", workspace).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var out []Incident
	err = json.Unmarshal(raw, &out)
	return out, err
}
func newID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
func event(ctx context.Context, tx *sql.Tx, workspace, project, kind, id string) error {
	return audit.Record(ctx, tx, workspace, project, kind, id)
}
func (s Store) Report(ctx context.Context, workspace, project, observed, expected, steps, key string) (string, error) {
	if !requestKey.MatchString(key) || !validText(observed) || !validText(expected) || !validText(steps) {
		return "", ErrInvalid
	}
	o := observation{Observed: Redact(observed), Expected: Redact(expected), Steps: Redact(steps), Seen: time.Now().UTC(), Frames: []Frame{}}
	if o.Observed == "" || o.Expected == "" {
		return "", ErrInvalid
	}
	o.Event = digest(key)
	v, _ := json.Marshal([]string{o.Observed, o.Expected, o.Steps})
	o.Group = "v1:" + digest(string(v))
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var scoped bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT FROM projects WHERE workspace_id=$1 AND id=$2)`, workspace, project).Scan(&scoped); err != nil {
		return "", err
	}
	if !scoped {
		return "", ErrUnmapped
	}
	id, inserted, err := insertObservation(ctx, tx, workspace, project, "manual", o)
	if err != nil {
		return "", err
	}
	var incidentID string
	if inserted {
		incidentID, err = apply(ctx, tx, id)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT i.id FROM incidents i JOIN incident_inbox b ON (i.workspace_id,i.project_id,i.source,i.group_key)=(b.workspace_id,b.project_id,b.source,b.group_key) WHERE b.id=$1 AND b.group_key=$2`, id, o.Group).Scan(&incidentID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrConflict
		}
	}
	if err != nil {
		return "", err
	}
	return incidentID, tx.Commit()
}

func insertObservation(ctx context.Context, tx *sql.Tx, workspace, project, source string, o observation) (int64, bool, error) {
	frames, err := json.Marshal(o.Frames)
	if err != nil {
		return 0, false, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO incident_inbox(workspace_id,project_id,source,event_key,group_key,observed,expected,steps,release,environment,frames,seen_at)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(workspace_id,project_id,source,event_key) DO NOTHING RETURNING id`,
		workspace, project, source, o.Event, o.Group, o.Observed, o.Expected, o.Steps, o.Release, o.Environment, string(frames), o.Seen).Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) {
		return id, err == nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM incident_inbox WHERE workspace_id=$1 AND project_id=$2 AND source=$3 AND event_key=$4`, workspace, project, source, o.Event).Scan(&id)
	return id, false, err
}

//go:embed sql/apply_observation.sql
var applyObservation string

// Caller owns the locked inbox row until commit, including the count and event update.
func apply(ctx context.Context, tx *sql.Tx, inbox int64) (string, error) {
	var id, workspace, project string
	err := tx.QueryRowContext(ctx, applyObservation, inbox).Scan(&id, &workspace, &project)
	if err != nil {
		return "", err
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: "Accepted observation assigned to its incident", EvidenceRef: strconv.FormatInt(inbox, 10)})
	return id, event(ctx, tx, workspace, project, "incident.observed", id)
}

func (s Store) processOne(ctx context.Context) error {
	var processed bool
	if err := storage.Coordinate(ctx, s.DB, "jobs.process_incident", struct{}{}, &processed); err != nil {
		return err
	}
	if !processed {
		return sql.ErrNoRows
	}
	return nil
}

func (s Store) Promote(ctx context.Context, workspace, project, id string, version int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM incidents WHERE workspace_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, workspace, project, id).Scan(&cur); err != nil {
		return err
	}
	var approved int64
	err = tx.QueryRowContext(ctx, `SELECT approved_version FROM incident_issues WHERE workspace_id=$1 AND project_id=$2 AND incident_id=$3`, workspace, project, id).Scan(&approved)
	if err == nil && approved == version {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cur != version || err == nil {
		return ErrConflict
	}
	var policyVersion int64
	err = tx.QueryRowContext(ctx, `INSERT INTO incident_issues(workspace_id,project_id,incident_id,installation_id,repository_id,marker,approved_version,status,policy_version)
	SELECT m.workspace_id,m.project_id,$3,installation_id,repository_id,$4,$5,'queued',p.policy_version FROM github_mappings m JOIN projects p ON p.id=m.project_id WHERE m.workspace_id=$1 AND m.project_id=$2 AND active AND p.allow_promotion AND NOT p.paused RETURNING policy_version`, workspace, project, id, newID(), version).Scan(&policyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnmapped
	}
	if err != nil {
		return err
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: "Mapped incident approved for issue creation", SubjectVersion: version, PolicyVersion: policyVersion})
	if _, err = tx.ExecContext(ctx, `UPDATE incidents SET version=version+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if err = event(ctx, tx, workspace, project, "incident.promotion_requested", id); err != nil {
		return err
	}
	return tx.Commit()
}
