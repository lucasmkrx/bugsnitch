// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/device"
	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/github"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func TestLocalRunTwoAttemptsAndFencedConflict(t *testing.T) {
	s := fixture(t)
	owner := login(t, s)
	repo := "fixture/batch"
	project := owner.post(t, "projects", object{"name": "Local batch", "repository": repo}, 201)["id"].(string)
	execSQL := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL("INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id) VALUES('personal',$1,30,31)", project)
	const stamp = "2026-11-21T10:00:00Z"
	doc := domain.Plan{Title: "Correct labels", Milestone: "Display"}
	sources := map[int]github.Issue{}
	for n := 1; n <= 3; n++ {
		i := github.Issue{ID: int64(40 + n), Number: int64(n), Title: fmt.Sprintf("Fix label %d", n), Body: "Verified fixture requirement", State: "open", UpdatedAt: stamp}
		sources[n] = i
		doc.Tickets = append(doc.Tickets, domain.Ticket{Number: n, Title: i.Title, Criteria: []string{"Show fixed label"}, Verification: "Check label.txt", EstimateHours: 1})
		execSQL(`INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,checked_at,selection_revision) VALUES('personal',$1,$2,$3,30,31,$4,$5,'open','current',now(),$6)`, project, n, i.ID, i.Title, i.RequirementsDigest(), stamp)
	}
	planID := owner.post(t, "plans", object{"projectId": project, "version": 1, "plan": doc}, 201)["id"].(string)
	owner.post(t, "plans/"+planID+"/approve", object{"version": 2}, 200)
	code := owner.post(t, "pairings", object{"name": "Batch fixture", "projectIds": []string{project}}, 201)["code"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var gate sync.Mutex
	active := map[string]bool{}
	both := make(chan struct{})
	var changePolicy atomic.Bool
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = routedTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" && r.Method == "GET" {
			n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/"+repo+"/issues/"))
			i, ok := sources[n]
			if err != nil || !ok {
				return nil, fmt.Errorf("unselected GitHub read")
			}
			b, _ := json.Marshal(object{"id": i.ID, "title": i.Title, "body": i.Body, "number": i.Number, "state": i.State, "updated_at": stamp, "html_url": fmt.Sprintf("https://github.com/%s/issues/%d", repo, n)})
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(b))}, nil
		}
		if r.URL.Host != "localhost:8787" {
			return nil, fmt.Errorf("unexpected external operation")
		}
		if r.URL.Path == "/api/v1/device/admission" {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			var a protocol.Admission
			if err = json.Unmarshal(b, &a); err != nil {
				return nil, err
			}
			if a.Operation == "turn_start" && a.Sequence == 2 && a.IssueNumber <= 2 {
				gate.Lock()
				if !active[a.AttemptID] {
					active[a.AttemptID] = true
					if len(active) == 2 {
						close(both)
					}
				}
				gate.Unlock()
				select {
				case <-both:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if a.Operation == "admit" && a.IssueNumber == 3 && changePolicy.CompareAndSwap(true, false) {
				if _, err = s.DB.Exec("UPDATE projects SET policy_version=policy_version+1 WHERE id=$1", project); err != nil {
					return nil, err
				}
			}
		}
		rec := httptest.NewRecorder()
		owner.handler.ServeHTTP(rec, r)
		return rec.Result(), nil
	})
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if err := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=test@fixture.invalid"}, args...)...).Run(); err != nil {
			t.Fatal(args, err)
		}
	}
	git("init")
	if err = os.WriteFile(filepath.Join(dir, "label.txt"), []byte("wrong\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "label.txt")
	git("commit", "-m", "baseline")
	git("remote", "add", "origin", "https://github.com/"+repo+".git")
	if err = device.RunCLI([]string{"pair", "--server", s.Origin, "--code", code}); err != nil {
		t.Fatal(err)
	}
	if err = device.RunCLI([]string{"register", "--project", project, "--path", dir}); err != nil {
		t.Fatal(err)
	}
	if err = runner.AddProject("personal", repo, dir, runner.Config{Files: []string{"label.txt"}, Validate: []string{"/usr/bin/grep", "-Fx", "fixed", "label.txt"}}); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err = os.Symlink(executable, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	start, finish, tstart, tend, lease := runner.BeforeStart, runner.AfterFinish, runner.BeforeTurn, runner.AfterTurn, runner.CheckWorker
	t.Cleanup(func() {
		runner.BeforeStart, runner.AfterFinish, runner.BeforeTurn, runner.AfterTurn, runner.CheckWorker = start, finish, tstart, tend, lease
	})
	runner.BeforeStart, runner.AfterFinish, runner.BeforeTurn, runner.AfterTurn, runner.CheckWorker = device.Admit, device.Finish, device.TurnStart, device.TurnEnd, device.CheckWorker
	var output bytes.Buffer
	if err = device.LocalRun(ctx, []string{"--project", project, "--limit", "2"}, &output); err != nil {
		t.Fatal(output.String(), err)
	}
	list, err := runner.Summaries()
	if err != nil || len(list) != 2 || list[0].ID == list[1].ID || list[0].IssueNumber == list[1].IssueNumber {
		t.Fatal(list, err)
	}
	for _, a := range list {
		full, err := runner.Get(a.ID)
		if err != nil || full.State != "completed" || full.LocalRunScope.Plan != planID || full.Branch != "bugsnitch/"+a.ID || len(full.Artifacts) != 4 || full.Artifacts[2].ExitCode == nil || *full.Artifacts[2].ExitCode != 0 {
			t.Fatal(full.Summary, err)
		}
	}
	if bytes.Count(output.Bytes(), []byte(`"event":"admitted"`)) != 2 || bytes.Count(output.Bytes(), []byte(`"state":"completed"`)) != 2 {
		t.Fatal(output.String())
	}
	changePolicy.Store(true)
	output.Reset()
	if err = device.LocalRun(ctx, []string{"--project", project, "--limit", "1"}, &output); err == nil || !strings.Contains(output.String(), `"state":"waiting"`) || strings.Contains(output.String(), `"event":"admitted"`) {
		t.Fatal("stale policy started a worker", output.String(), err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM admission").Scan(&count); err != nil || count != 2 {
		t.Fatal("incorrect claim count", count, err)
	}
	if err = device.LocalRun(ctx, []string{"--project", project, "--limit", "1"}, io.Discard); err == nil {
		t.Fatal("restarted an existing conflicted ticket")
	}
}
