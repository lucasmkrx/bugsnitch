package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func TestHealthyHeadCannotAuthorizeUnverifiedCombination(t *testing.T) {
	db, _ := scratchDB(t)
	s := Store{DB: db}
	obs := github.PullObservation{Checks: "SUCCESS", CheckCount: 1, Review: "APPROVED", IssueState: "OPEN", State: "OPEN", Base: "base"}
	p, err := s.save(context.Background(), "personal", "p", 4, 3, 0, 10, 11, "example/test", obs)
	if err != nil {
		t.Fatal(err)
	}
	if p.AutoMerge || !strings.Contains(strings.Join(p.Blockers, " "), "Combined validation evidence is missing") {
		t.Fatal(p)
	}
}
func scratchDB(t *testing.T) (*sql.DB, func(string, ...any)) {
	t.Helper()
	dsn := os.Getenv("BUGSNITCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BUGSNITCH_TEST_DATABASE_URL to a disposable PostgreSQL URL")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	var b [12]byte
	rand.Read(b[:])
	schema := fmt.Sprintf("test_pull_%x", b)
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	ctx := context.Background()
	db, err := storage.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO projects(id,workspace_id,name,repository) VALUES('p','personal','Fixture','example/test');INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal','p',10,11)`)
	return db, run
}
func TestPostgresInvalidateEvidenceOnSubjectChange(t *testing.T) {
	ctx := context.Background()
	db, run := scratchDB(t)
	s := Store{DB: db}
	obs := github.PullObservation{State: "OPEN", IssueState: "OPEN", Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), Target: "main", Criteria: "v1", Review: "APPROVED", Checks: "SUCCESS", CheckCount: 2}
	var version int64
	save := func() Pull {
		t.Helper()
		p, err := s.save(ctx, "personal", "p", 4, 3, version, 10, 11, "example/test", obs)
		if err != nil {
			t.Fatal(err)
		}
		version = p.Version
		return p
	}
	save()
	for _, change := range []func(){func() { obs.Head = strings.Repeat("f", 40) }, func() { obs.Base = strings.Repeat("e", 40) }, func() { obs.Criteria = "v2" }, func() { run("UPDATE projects SET version=version+1,policy_version=policy_version+1 WHERE id='p'") }} {
		run(`INSERT INTO pull_evidence(workspace_id,project_id,number,version,kind,reference) VALUES('personal','p',4,$1,'combined_validation',$2)`, version, strings.Repeat("5", 64))
		if p := save(); p.Validation == "" {
			t.Fatal("matching evidence missing")
		}
		before := version
		change()
		p := save()
		if p.Version != before+1 || p.Validation != "" || p.AutoMerge {
			t.Fatal("stale evidence survived subject change", p)
		}
		if _, err := s.save(ctx, "personal", "p", 4, 3, before, 10, 11, "example/test", obs); !errors.Is(err, ErrConflict) {
			t.Fatal("stale refresh was accepted", err)
		}
	}
	mine, err := s.List(ctx, "elsewhere")
	if err != nil || len(mine) != 0 {
		t.Fatal("cross-workspace leak", err)
	}
}

func TestQueueAndPassingChecksCannotReplaceDecisionForTarget(t *testing.T) {
	db, run := scratchDB(t)
	s := Store{DB: db}
	observed := github.PullObservation{Queue: &github.QueueObservation{OwnerType: "Organization", Present: true, Rules: &github.RuleSnapshot{QueueRequired: true, RequiredChecks: 2, AppBoundChecks: 2}}, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), Target: "disposable", State: "OPEN", IssueState: "OPEN", Review: "APPROVED", Checks: "SUCCESS", CheckCount: 2}
	for i, target := range []string{"disposable", "production"} {
		observed.Target = target
		number := int64(i + 4)
		p, err := s.save(context.Background(), "personal", "p", number, 3, 0, 10, 11, "example/test", observed)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"combined_validation", "independent_review"} {
			run("INSERT INTO pull_evidence(workspace_id,project_id,number,version,kind,reference) VALUES('personal','p',$1,$2,$3,'test-artifact')", number, p.Version, kind)
		}
		p, err = s.save(context.Background(), "personal", "p", number, 3, p.Version, 10, 11, "example/test", observed)
		if err != nil || p.Validation == "" || p.IndependentReview == "" {
			t.Fatal(err, p)
		}
		if p.AutoMerge || !strings.Contains(strings.Join(p.Blockers, " "), "approved target") {
			t.Fatalf("accepted a queue without a target-bound decision for %s", target)
		}
	}
}

func TestHandoverRequiresExactMergedSubjectAndReplaysOnce(t *testing.T) {
	db, run := scratchDB(t)
	run(`INSERT INTO devices(id,workspace_id,name,platform) VALUES('worker','personal','Mac','test');
	    INSERT INTO device_projects VALUES('personal','worker','p');
	    INSERT INTO worker_starts(id,workspace_id,project_id,device_id,issue_number,request_id,requested_plan,policy_version,
	        github_id,digest,revision,title,status) VALUES('start','personal','p','worker',7,'start_7','',1,71,'digest','rev','Plan ticket','started');
	    WITH turn AS (INSERT INTO events(workspace_id,kind,subject_id,actor_source,actor_id)
	        VALUES('personal','local_model.started','start','device','worker') RETURNING seq)
	        INSERT INTO model_turn_metering(event_seq,model,input_tokens,output_tokens) SELECT seq,'unpriced-planner',60,10 FROM turn;
	    INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,candidate_sha,phase,state,version)
	       VALUES ('attempt','personal','p','worker',7,'a','b','wrap_up','completed',1),
	              ('other','personal','p','worker',8,'a','b','wrap_up','completed',1);
	    INSERT INTO model_price_history(workspace_id,model,version,effective_at,input_per_million_usd,output_per_million_usd)
	       VALUES('personal','model',1,now()-interval '1 day',2,4);
	    WITH turns AS (INSERT INTO events(workspace_id,project_id,kind,subject_id,actor_source,actor_id)
	       VALUES('personal','p','admission.turn_start','attempt','device','worker'),
	             ('personal','p','admission.turn_start','other','device','worker') RETURNING seq)
	       INSERT INTO model_turn_metering(event_seq,model,input_tokens,output_tokens) SELECT seq,'model',1000,500 FROM turns;
	    INSERT INTO events(workspace_id,project_id,kind,subject_id,actor_source,actor_id)
	       VALUES('personal','p','admission.turn_start','attempt','device','worker')`)
	s := Store{DB: db}
	ctx := audit.WithActor(t.Context(), audit.Owner, "owner")
	observation := github.PullObservation{State: "OPEN", Target: "main"}
	pull, err := s.save(ctx, "personal", "p", 9, 7, 0, 10, 11, "example/test", observation)
	if err != nil {
		t.Fatal(err)
	}
	h := Handover{
		Version: pull.Version, Target: "main", MergedSHA: strings.Repeat("a", 40), Environment: "staging",
		Build: "build-7", Digest: strings.Repeat("b", 64), Summary: "Shipping threshold corrected.", TestSteps: "Check a $50 order.",
	}
	if err := s.Handover(ctx, "personal", "p", 9, h); !errors.Is(err, ErrConflict) {
		t.Fatal("open PR accepted", err)
	}
	observation.State, observation.MergedSHA = "MERGED", h.MergedSHA
	pull, err = s.save(ctx, "personal", "p", 9, 7, pull.Version, 10, 11, "example/test", observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Handover(ctx, "personal", "p", 9, h); !errors.Is(err, ErrConflict) {
		t.Fatal("stale inspection accepted", err)
	}
	h.Version = pull.Version
	for i := range 2 {
		if err := s.Handover(ctx, "personal", "p", 9, h); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			run(`INSERT INTO model_turn_metering(event_seq,model,input_tokens,output_tokens)
				SELECT max(seq),'model',50000,50 FROM events WHERE kind='admission.turn_start' AND subject_id='attempt'`)
		}
	}
	pulls, err := s.List(ctx, "personal")
	if err != nil || len(pulls) != 1 || len(pulls[0].Handovers) != 1 || pulls[0].Handovers[0].Digest != h.Digest {
		t.Fatal("handover projection", pulls, err)
	}
	cost := pulls[0].Handovers[0].Cost
	if cost == nil || cost.AdmittedTurns != 3 || cost.MeteredTurns != 2 || cost.PricedTurns != 1 ||
		cost.InputTokens != 1060 || cost.OutputTokens != 510 || cost.EstimatedUSD == nil || *cost.EstimatedUSD != 0.004 {
		t.Fatalf("handover must snapshot only this ticket's known cost and coverage: %+v", cost)
	}
	h.TestSteps = "Changed"
	if err := s.Handover(ctx, "personal", "p", 9, h); !errors.Is(err, ErrConflict) {
		t.Fatal("handover overwritten", err)
	}
}

func TestWatchSchedulerIgnoresUnwatchedAndClosedPulls(t *testing.T) {
	db, run := scratchDB(t)
	s := Store{DB: db}
	ctx := t.Context()
	for n, state := range map[int64]string{5: "OPEN", 6: "OPEN", 7: "MERGED"} {
		observation := github.PullObservation{State: state, Target: "main"}
		if _, err := s.save(ctx, "personal", "p", n, 4, 0, 10, 11, "example/test", observation); err != nil {
			t.Fatal(err)
		}
	}
	run("UPDATE pull_observations SET watching=true WHERE number IN (6,7)")
	job, err := storage.ClaimJob(ctx, db, "pull_watch")
	if err != nil || job.Number != 6 {
		t.Fatal("wrong watch job", job, err)
	}
	if _, err := storage.ClaimJob(ctx, db, "pull_watch"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("another watch job was due", err)
	}
}
