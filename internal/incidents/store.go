package incidents

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	rows, err := s.DB.QueryContext(ctx, `SELECT i.id,i.project_id,i.observed,i.expected,i.steps,i.source,i.state,i.occurrences,
	i.first_seen,i.last_seen,i.version,x.number,x.github_state,x.status,nullif(x.reason,''),
	CASE WHEN i.source='sentry' THEN i.group_key END,nullif(latest.environment,''),nullif(latest.release,''),latest.frames
	FROM incidents i LEFT JOIN incident_issues x ON i.id=x.incident_id
	LEFT JOIN LATERAL(SELECT environment,release,frames FROM incident_inbox b WHERE b.workspace_id=i.workspace_id AND b.project_id=i.project_id AND b.source=i.source AND b.group_key=i.group_key ORDER BY b.seen_at DESC,b.id DESC LIMIT 1) latest ON true
	WHERE i.workspace_id=$1 ORDER BY i.last_seen DESC,i.id LIMIT 500`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		var i Incident
		var frames []byte
		if err = rows.Scan(&i.ID, &i.ProjectID, &i.Observed, &i.Expected, &i.Steps, &i.Source, &i.Status, &i.Occurrences, &i.FirstSeen, &i.LastSeen, &i.Version, &i.IssueNumber, &i.IssueState, &i.Promotion, &i.PromotionReason, &i.ProviderGroup, &i.Environment, &i.Release, &frames); err != nil {
			return nil, err
		}
		if len(frames) > 0 {
			if err = json.Unmarshal(frames, &i.Frames); err != nil {
				return nil, err
			}
		}
		if i.ProviderGroup != nil {
			group := *i.ProviderGroup
			*i.ProviderGroup = group[strings.LastIndex(group, "/")+1:]
		}
		i.Title = strings.Split(i.Observed, "\n")[0]
		r := []rune(i.Title)
		if len(r) > 110 {
			i.Title = string(r[:109]) + "…"
		}
		out = append(out, i)
	}
	return out, rows.Err()
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

// Caller owns the locked inbox row until commit, including the count and event update.
func apply(ctx context.Context, tx *sql.Tx, inbox int64) (string, error) {
	var id, workspace, project string
	err := tx.QueryRowContext(ctx, `INSERT INTO incidents(id,workspace_id,project_id,source,group_key,observed,expected,steps,first_seen,last_seen,occurrences)
	SELECT $2,workspace_id,project_id,source,group_key,observed,expected,steps,seen_at,seen_at,1 FROM incident_inbox WHERE id=$1 AND NOT processed
	ON CONFLICT(workspace_id,project_id,source,group_key) DO UPDATE SET
	first_seen=least(incidents.first_seen,excluded.first_seen),last_seen=greatest(incidents.last_seen,excluded.last_seen),
	observed=CASE WHEN incidents.redacted_at IS NOT NULL AND excluded.last_seen>=incidents.last_seen THEN excluded.observed ELSE incidents.observed END,
 expected=CASE WHEN incidents.redacted_at IS NOT NULL AND excluded.last_seen>=incidents.last_seen THEN excluded.expected ELSE incidents.expected END,
 steps=CASE WHEN incidents.redacted_at IS NOT NULL AND excluded.last_seen>=incidents.last_seen THEN excluded.steps ELSE incidents.steps END,
 redacted_at=CASE WHEN excluded.last_seen>=incidents.last_seen THEN NULL ELSE incidents.redacted_at END,
 occurrences=incidents.occurrences+1,version=incidents.version+1
	RETURNING id,workspace_id,project_id`, inbox, newID()).Scan(&id, &workspace, &project)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE incident_inbox SET processed=true WHERE id=$1`, inbox); err != nil {
		return "", err
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: "Accepted observation assigned to its incident", EvidenceRef: strconv.FormatInt(inbox, 10)})
	return id, event(ctx, tx, workspace, project, "incident.observed", id)
}

func (s Store) processOne(ctx context.Context) error {
	ctx = audit.WithActor(ctx, audit.Service, "incident-worker")
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM incident_inbox WHERE NOT processed ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id); err != nil {
		return err
	}
	if _, err = apply(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s Store) SentryWebhook(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := signedBody(w, r, secret, "Sentry-Hook-Signature", "")
		if !ok {
			return
		}
		if r.Header.Get("Sentry-Hook-Resource") != "error" {
			http.Error(w, "subscribe to error.created", 422)
			return
		}
		o, err := normalizeError(b)
		if err != nil {
			http.Error(w, "unsupported error event", 422)
			return
		}
		ctx := r.Context()
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			http.Error(w, "intake unavailable", 503)
			return
		}
		defer tx.Rollback()
		var workspace, project string
		err = tx.QueryRowContext(ctx, `SELECT m.workspace_id,m.project_id FROM sentry_mappings m JOIN projects p ON (p.workspace_id,p.id)=(m.workspace_id,m.project_id) WHERE m.installation_uuid=$1 AND m.provider_project=$2 AND m.active FOR UPDATE OF p FOR SHARE OF m`, o.Installation, o.ProviderProject).Scan(&workspace, &project)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "unmapped installation or project", 403)
			return
		}
		if err != nil {
			http.Error(w, "intake unavailable", 503)
			return
		}
		// Serialize rate checks per project. Retries of accepted events never consume allowance.
		var limited bool
		err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT FROM incident_inbox WHERE workspace_id=$1 AND project_id=$2 AND source='sentry' AND event_key=$3) AND
		(SELECT count(*) FROM (SELECT 1 FROM incident_inbox WHERE workspace_id=$1 AND project_id=$2 AND source='sentry' AND accepted_at>now()-interval '1 minute' LIMIT 600) recent)>=600`, workspace, project, o.Event).Scan(&limited)
		if err != nil {
			http.Error(w, "intake unavailable", 503)
			return
		}
		if limited {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "project intake limit", 429)
			return
		}
		inbox, fresh, err := insertObservation(ctx, tx, workspace, project, "sentry", o)
		if err == nil && fresh {
			ctx = audit.WithActor(ctx, audit.Provider, "sentry/"+o.Installation)
			ctx = audit.WithDetails(ctx, audit.Details{Reason: "Signed Sentry event matched the authorized project", EvidenceRef: o.Event})
			err = event(ctx, tx, workspace, project, "sentry.event_accepted", strconv.FormatInt(inbox, 10))
		}
		if err != nil || tx.Commit() != nil {
			http.Error(w, "intake unavailable", 503)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
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
