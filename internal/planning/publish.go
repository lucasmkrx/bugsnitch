// SPDX-License-Identifier: MPL-2.0
package planning

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

type Store struct{ DB *sql.DB }

var ErrConflict = errors.New("publication scope or version changed")
var ErrUnavailable = errors.New("active GitHub App mapping required")

func id() string { b := make([]byte, 16); rand.Read(b); return hex.EncodeToString(b) }
func (s Store) Propose(ctx context.Context, ws, p, key string, version int64, doc domain.Plan) (string, error) {
	if _, err := doc.Order(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var v int64
	if err = tx.QueryRowContext(ctx, "SELECT version FROM projects WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, p).Scan(&v); err != nil {
		return "", err
	}
	var saved string
	var same bool
	err = tx.QueryRowContext(ctx, "SELECT id,(document=$1::jsonb AND project_id=$2 AND subject_version=$3) FROM publications WHERE workspace_id=$4 AND request_id=$5", raw, p, version, ws, key).Scan(&saved, &same)
	if err == nil {
		if same {
			return saved, nil
		}
		return "", ErrConflict
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	if v != version {
		return "", ErrConflict
	}
	saved = id()
	if _, err = tx.ExecContext(ctx, "INSERT INTO publications(id,workspace_id,project_id,request_id,document,subject_version,status) VALUES($1,$2,$3,$4,$5,$6,'proposed')", saved, ws, p, key, raw, v); err != nil {
		return "", err
	}
	if err = audit.Record(ctx, tx, ws, p, "publication.proposed", saved); err != nil {
		return "", err
	}
	return saved, tx.Commit()
}
func (s Store) Approve(ctx context.Context, ws, pub string, version int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var p, status string
	var raw []byte
	var current, subject, projectV int64
	var paused, allowed bool
	var policyV int64
	err = tx.QueryRowContext(ctx, `SELECT x.project_id,x.status,x.version,x.subject_version,x.document,p.version,p.paused,p.allow_publication,p.policy_version FROM publications x JOIN projects p ON p.id=x.project_id WHERE x.id=$1 AND x.workspace_id=$2 FOR UPDATE OF x,p`, pub, ws).Scan(&p, &status, &current, &subject, &raw, &projectV, &paused, &allowed, &policyV)
	if err != nil {
		return err
	}
	if status != "proposed" {
		if version == 1 {
			return nil
		}
		return ErrConflict
	}
	if current != version || projectV != subject || paused || !allowed {
		return ErrConflict
	}
	var install, repo int64
	err = tx.QueryRowContext(ctx, "SELECT installation_id,repository_id FROM github_mappings WHERE workspace_id=$1 AND project_id=$2 AND active FOR SHARE", ws, p).Scan(&install, &repo)
	if err == sql.ErrNoRows {
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	var doc domain.Plan
	if err = json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	order, err := doc.Order()
	if err != nil {
		return err
	}
	for pos, n := range order {
		if _, err = tx.ExecContext(ctx, "INSERT INTO planned_issues(workspace_id,project_id,publication_id,local_number,position,marker) VALUES($1,$2,$3,$4,$5,$6)", ws, p, pub, n, pos, id()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE publications SET status='publishing',installation_id=$1,repository_id=$2,policy_version=$4,version=version+1 WHERE id=$3", install, repo, pub, policyV); err != nil {
		return err
	}
	ctx = audit.WithDetails(ctx, audit.Details{Reason: "Approved publication for the selected repository", PolicyVersion: policyV, SubjectVersion: version})
	if err = audit.Record(ctx, tx, ws, p, "publication.approved", pub); err != nil {
		return err
	}
	return tx.Commit()
}

type issueWriter interface {
	Close()
	Create(context.Context, string, string) (github.Issue, error)
	Find(context.Context, string, time.Time) (*github.Issue, error)
}
type openRepo func(context.Context, int64, int64, string) (issueWriter, error)
type job struct {
	ws, p, pub, repo, marker, status string
	local, tries                     int
	install, repository              int64
	created                          time.Time
	doc                              domain.Plan
}

func (s Store) SyncIssue(ctx context.Context, app *github.App, claim storage.JobClaim) error {
	ctx = audit.WithActor(ctx, audit.Service, "plan-publisher")
	return s.publishClaimed(ctx, func(c context.Context, i, r int64, n string) (issueWriter, error) {
		return app.Repository(c, i, r, n)
	}, claim)
}

func (s Store) step(ctx context.Context, open openRepo) error {
	claim, err := storage.ClaimJob(ctx, s.DB, "planned_issue")
	if err != nil {
		return err
	}
	return s.publishClaimed(ctx, open, claim)
}

func (s Store) publishClaimed(ctx context.Context, open openRepo, claim storage.JobClaim) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var j job
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT i.workspace_id,i.project_id,i.publication_id,i.local_number,i.marker,i.status,i.tries,i.created_at,x.document,m.installation_id,m.repository_id,p.repository FROM planned_issues i JOIN publications x ON x.id=i.publication_id JOIN projects p ON p.id=i.project_id JOIN github_mappings m ON (m.workspace_id,m.project_id,m.installation_id,m.repository_id)=(x.workspace_id,x.project_id,x.installation_id,x.repository_id)
 WHERE i.status IN('queued','reconciling') AND x.status='publishing' AND m.active AND NOT p.paused AND (i.status='reconciling' OR (p.allow_publication AND x.policy_version=p.policy_version)) AND (i.workspace_id,i.project_id,i.publication_id,i.local_number)=($1,$2,$3,$4)
 AND i.next_run=$5::timestamptz AND i.next_run>clock_timestamp() AND NOT EXISTS(SELECT FROM planned_issues prev WHERE prev.publication_id=i.publication_id AND prev.position<i.position AND prev.status<>'linked') FOR UPDATE OF i SKIP LOCKED FOR SHARE OF m SKIP LOCKED`,
		claim.Workspace, claim.Project, claim.Item, claim.Number, claim.Fence).Scan(&j.ws, &j.p, &j.pub, &j.local, &j.marker, &j.status, &j.tries, &j.created, &raw, &j.install, &j.repository, &j.repo)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &j.doc); err != nil {
		return err
	}
	repo, err := open(ctx, j.install, j.repository, j.repo)
	if err != nil {
		return deferJob(ctx, tx, j, "Repository authorization unavailable", err)
	}
	defer repo.Close()
	if j.status == "reconciling" {
		found, e := repo.Find(ctx, j.tag(), j.created)
		if e != nil || found == nil {
			return deferJob(ctx, tx, j, "Awaiting canonical issue after an uncertain write", e)
		}
		return s.link(ctx, tx, j, *found)
	}
	var intended string
	for _, ticket := range j.doc.Tickets {
		if ticket.Number == j.local {
			title, body, err := j.text(ctx, tx, ticket)
			if err != nil {
				return err
			}
			intended = requirements(github.Issue{Title: title, Body: body})
			break
		}
	}
	if intended == "" {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE planned_issues SET creation_digest=$3,status='reconciling',reason='Confirming issue creation',next_run=now()+interval '1 minute' WHERE publication_id=$1 AND local_number=$2", j.pub, j.local, intended); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.create(ctx, j, repo)
}
func (j job) tag() string { return "<!-- bugsnitch:plan:" + j.marker + " -->" }
func (s Store) create(ctx context.Context, j job, repo issueWriter) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ok bool
	err = tx.QueryRowContext(ctx, `SELECT i.status='reconciling' AND m.active AND NOT p.paused AND p.allow_publication AND x.policy_version=p.policy_version AND x.status='publishing' FROM planned_issues i JOIN publications x ON x.id=i.publication_id JOIN github_mappings m ON (m.workspace_id,m.project_id,m.installation_id,m.repository_id)=(x.workspace_id,x.project_id,x.installation_id,x.repository_id) JOIN projects p ON p.id=x.project_id WHERE i.publication_id=$1 AND i.local_number=$2 FOR SHARE OF m,p,x FOR UPDATE OF i`, j.pub, j.local).Scan(&ok)
	if err != nil || !ok {
		return err
	}
	for _, ticket := range j.doc.Tickets {
		if ticket.Number != j.local {
			continue
		}
		title, content, err := j.text(ctx, tx, ticket)
		if err != nil {
			return err
		}
		issue, e := repo.Create(ctx, title, content)
		if e != nil {
			j.status = "reconciling"
			return deferJob(ctx, tx, j, "Checking for an accepted GitHub write", e)
		}
		return s.link(ctx, tx, j, issue)
	}
	return ErrConflict
}
func (j job) text(ctx context.Context, tx *sql.Tx, ticket domain.Ticket) (string, string, error) {
	var body strings.Builder
	fmt.Fprintln(&body, "## Acceptance criteria")
	for _, c := range ticket.Criteria {
		fmt.Fprintln(&body, "- "+incidents.Redact(c))
	}
	fmt.Fprintf(&body, "\n## Verification\n\n%s\n", incidents.Redact(ticket.Verification))
	for _, dep := range ticket.Dependencies {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT canonical_number FROM planned_issues WHERE publication_id=$1 AND local_number=$2 AND status='linked'", j.pub, dep).Scan(&n); err != nil {
			return "", "", err
		}
		fmt.Fprintf(&body, "\nDepends on #%d.\n", n)
	}
	content := strings.ReplaceAll(body.String(), "@", "＠") + "\n" + j.tag()
	return strings.ReplaceAll(incidents.Redact(ticket.Title), "@", "＠"), content, nil
}

func deferJob(ctx context.Context, tx *sql.Tx, j job, reason string, e error) error {
	next := time.Minute << min(j.tries, 6)
	var api *github.APIError
	if errors.As(e, &api) && api.RetryAfter > next {
		next = api.RetryAfter
	}
	if j.tries >= 7 {
		j.status = "blocked"
		reason += ". Owner reconciliation required."
	}
	if _, err := tx.ExecContext(ctx, "UPDATE planned_issues SET status=$1,reason=$2,tries=tries+1,next_run=now()+($3::integer*interval '1 second') WHERE publication_id=$4 AND local_number=$5", j.status, reason, int(next.Seconds()), j.pub, j.local); err != nil {
		return err
	}
	return tx.Commit()
}
func (s Store) link(ctx context.Context, tx *sql.Tx, j job, i github.Issue) error {
	if i.Number < 1 || i.ID < 1 || len(i.PullRequest) > 0 || (i.State != "open" && i.State != "closed") {
		j.status = "blocked"
		return deferJob(ctx, tx, j, "Canonical issue identity or state unavailable", nil)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE planned_issues SET status='linked',canonical_number=$3,github_id=$4,source_state=$5,reason='' WHERE publication_id=$1 AND local_number=$2", j.pub, j.local, i.Number, i.ID, i.State); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,checked_at,next_check,selection_revision)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'current',now(),now()+interval '5 minutes',$10)`, j.ws, j.p, i.Number, i.ID, j.install, j.repository, issueTitle(i.Title), requirements(i), i.State, selectionRevision(i)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, j.ws, j.p, "plan.issue_linked", j.pub); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT local_number,canonical_number FROM planned_issues WHERE publication_id=$1 AND status='linked'", j.pub)
	if err != nil {
		return err
	}
	numbers := map[int]int{}
	for rows.Next() {
		var local, num int
		if err = rows.Scan(&local, &num); err != nil {
			rows.Close()
			return err
		}
		numbers[local] = num
	}
	e := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(numbers) == len(j.doc.Tickets) {
		// This new canonical proposal still needs the owner's approval before admission.
		var ignored int
		if err = tx.QueryRowContext(ctx, "SELECT version FROM projects WHERE workspace_id=$1 AND id=$2 FOR UPDATE", j.ws, j.p).Scan(&ignored); err != nil {
			return err
		}
		for k, t := range j.doc.Tickets {
			j.doc.Tickets[k].Number = numbers[t.Number]
			for n, dep := range t.Dependencies {
				j.doc.Tickets[k].Dependencies[n] = numbers[dep]
			}
		}
		order, err := j.doc.Order()
		if err != nil {
			return err
		}
		doc, _ := json.Marshal(j.doc)
		sorted, _ := json.Marshal(order)
		planID := id()
		if _, err = tx.ExecContext(ctx, "INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order) SELECT $1,$2,$3,coalesce(max(version),0)+1,$4,$5 FROM plan_versions WHERE project_id=$3", planID, j.ws, j.p, doc, sorted); err != nil {
			return err
		}
		if err = BindIssues(ctx, tx, j.ws, j.p, planID, j.doc); err != nil {
			return err
		}
		// A human can edit an accepted issue before its create response is reconciled.
		// Only the exact content submitted under the approved publication binds this generated proposal.
		if _, err = tx.ExecContext(ctx, `DELETE FROM plan_issue_bindings b USING planned_issues i,planned_issue_state z WHERE b.plan_id=$1 AND i.publication_id=$2 AND i.canonical_number=b.number AND (z.workspace_id,z.project_id,z.number)=(b.workspace_id,b.project_id,b.number) AND i.creation_digest IS DISTINCT FROM z.digest`, planID, j.pub); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE publications SET plan_id=$1,status='published',version=version+1 WHERE id=$2", planID, j.pub); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE projects SET version=version+1 WHERE id=$1", j.p); err != nil {
			return err
		}
		if err = audit.Record(ctx, tx, j.ws, j.p, "plan.proposed", planID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
