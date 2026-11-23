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
	exerciseParallelLocalRun(t, 2, 2, 2)
}

func TestLocalRunTwoTicketsShareOneDeviceTurn(t *testing.T) {
	exerciseParallelLocalRun(t, 2, 2, 1)
}

func TestLocalRunFourIndependentAttempts(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		localTurns, deviceTurns int
	}{
		{"four_turns", 4, 4},
		{"two_local_turns", 2, 4},
		{"two_hosted_device_turns", 4, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exerciseParallelLocalRun(t, 4, tc.localTurns, tc.deviceTurns)
		})
	}
}

func exerciseParallelLocalRun(t *testing.T, parallel, localTurns, deviceTurns int) {
	t.Helper()
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
	var files []string
	for n := 1; n <= parallel+1; n++ {
		name := fmt.Sprintf("label-%d.txt", n)
		files = append(files, name)
		i := github.Issue{ID: int64(40 + n), Number: int64(n), Title: fmt.Sprintf("Fix label %d", n), Body: "Verified fixture requirement", State: "open", UpdatedAt: stamp}
		sources[n] = i
		doc.Tickets = append(doc.Tickets, domain.Ticket{Number: n, Title: i.Title, Criteria: []string{"Show fixed label in " + name}, Verification: "Check " + name, EstimateHours: 1})
		execSQL(`INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id,title,digest,source_state,availability,checked_at,selection_revision) VALUES('personal',$1,$2,$3,30,31,$4,$5,'open','current',now(),$6)`, project, n, i.ID, i.Title, i.RequirementsDigest(), stamp)
	}
	planID := owner.post(t, "plans", object{"projectId": project, "version": 1, "plan": doc}, 201)["id"].(string)
	owner.post(t, "plans/"+planID+"/approve", object{"version": 2}, 200)
	code := owner.post(t, "pairings", object{"name": "Batch fixture", "projectIds": []string{project}}, 201)["code"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var gate sync.Mutex
	admitted := map[string]bool{}
	firstTurns := map[string]bool{}
	allStarted := make(chan struct{})
	var observation sync.Once
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
		var a protocol.Admission
		if r.URL.Path == "/api/v1/device/admission" {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			if err = json.Unmarshal(b, &a); err != nil {
				return nil, err
			}
			if a.Operation == "admit" && a.IssueNumber == parallel+1 && changePolicy.CompareAndSwap(true, false) {
				if _, err = s.DB.Exec("UPDATE projects SET policy_version=policy_version+1 WHERE id=$1", project); err != nil {
					return nil, err
				}
			}
		}
		rec := httptest.NewRecorder()
		owner.handler.ServeHTTP(rec, r)
		// Hold the first planning turns until every ticket is active and the
		// smaller of the two model-turn budgets is fully occupied.
		firstTurn := a.Operation == "turn_start" && a.Sequence == 2
		if (firstTurn || a.Operation == "admit") && rec.Code == http.StatusOK {
			gate.Lock()
			if firstTurn {
				firstTurns[a.AttemptID] = true
			} else {
				admitted[a.AttemptID] = true
			}
			if len(admitted) == parallel && len(firstTurns) >= min(localTurns, deviceTurns) {
				observation.Do(func() {
					var tickets, turns int
					err := s.DB.QueryRow("SELECT count(*) FILTER (WHERE active), count(*) FILTER (WHERE in_turn) FROM admission WHERE project_id=$1", project).Scan(&tickets, &turns)
					if err != nil || tickets != parallel || turns != min(localTurns, deviceTurns) {
						t.Errorf("unexpected concurrent reservations: tickets=%d, turns=%d, err=%v", tickets, turns, err)
					}
					if err := device.RunCLI([]string{"next", "--project", project}); err == nil || !strings.Contains(err.Error(), "workspace implementation slots") {
						t.Errorf("extra issue remained eligible at ticket capacity: %v", err)
					}
					t.Logf("simultaneous reservations on one device: tickets=%d model_turns=%d", tickets, turns)
					close(allStarted)
				})
			}
			gate.Unlock()
			if firstTurn {
				select {
				case <-allStarted:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
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
	for _, name := range files {
		if err = os.WriteFile(filepath.Join(dir, name), []byte("wrong\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(append([]string{"add", "--"}, files...)...)
	git("commit", "-m", "baseline")
	git("remote", "add", "origin", "https://github.com/"+repo+".git")
	if err = device.RunCLI([]string{"pair", "--server", s.Origin, "--code", code}); err != nil {
		t.Fatal(err)
	}
	if err = device.RunCLI([]string{"register", "--project", project, "--path", dir}); err != nil {
		t.Fatal(err)
	}
	if err = runner.AddProject("personal", repo, dir, runner.Config{Files: files, Validate: append([]string{"/usr/bin/grep", "-Fxq", "fixed"}, files...)}); err != nil {
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
	args := []string{"--project", project, "--limit", strconv.Itoa(parallel)}
	if parallel > 2 {
		if _, err := runner.SetLocalLimits(parallel, localTurns); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--parallel", strconv.Itoa(parallel))
	}
	if parallel > 2 || deviceTurns != 2 {
		limits := owner.post(t, "limits", object{"tickets": parallel, "turns": parallel, "version": 1}, 200)
		var deviceID string
		if err := s.DB.QueryRow("SELECT id FROM devices").Scan(&deviceID); err != nil {
			t.Fatal(err)
		}
		owner.post(t, "devices/"+deviceID+"/limits", object{"turns": deviceTurns, "version": limits["version"]}, 200)
	}
	if err = device.LocalRun(ctx, args, &output); err != nil {
		t.Fatal(output.String(), err)
	}
	list, err := runner.Summaries()
	if err != nil || len(list) != parallel || len(firstTurns) != parallel {
		t.Fatal(list, err)
	}
	worktrees := map[string]bool{}
	addressed := map[int]bool{}
	for _, a := range list {
		full, err := runner.Get(a.ID)
		if err != nil || full.State != "completed" || full.LocalRunScope.Plan != planID || full.Branch != "bugsnitch/"+a.ID || len(full.Artifacts) != 4 || full.Artifacts[2].ExitCode == nil || *full.Artifacts[2].ExitCode != 0 || full.Artifacts[3].Thread == full.Artifacts[1].Thread {
			t.Fatal(full.Summary, err)
		}
		if worktrees[full.Worktree] || addressed[a.IssueNumber] || !firstTurns[a.ID] {
			t.Fatal("duplicate worktree or issue, or missing concurrent turn", full.Summary)
		}
		worktrees[full.Worktree], addressed[a.IssueNumber] = true, true
		wantPath := fmt.Sprintf("label-%d.txt", a.IssueNumber)
		if changed := full.Artifacts[1].ChangedFiles; len(changed) != 1 || changed[0] != wantPath {
			t.Fatalf("issue %d changed %v, want only %s", a.IssueNumber, changed, wantPath)
		}
		t.Logf("issue #%d validated and reviewed in %s at %.12s", a.IssueNumber, full.Branch, full.CandidateSHA)
	}
	if bytes.Count(output.Bytes(), []byte(`"event":"admitted"`)) != parallel || bytes.Count(output.Bytes(), []byte(`"state":"completed"`)) != parallel {
		t.Fatal(output.String())
	}
	changePolicy.Store(true)
	output.Reset()
	if err = device.LocalRun(ctx, []string{"--project", project, "--limit", "1"}, &output); err == nil || !strings.Contains(output.String(), `"state":"waiting"`) || strings.Contains(output.String(), `"event":"admitted"`) {
		t.Fatal("stale policy started a worker", output.String(), err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM admission").Scan(&count); err != nil || count != parallel {
		t.Fatal("incorrect claim count", count, err)
	}
	if err = device.LocalRun(ctx, []string{"--project", project, "--limit", "1"}, io.Discard); err == nil {
		t.Fatal("restarted an existing conflicted ticket")
	}
}
