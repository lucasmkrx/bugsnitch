// SPDX-License-Identifier: MPL-2.0
package approvalpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAuthenticatedPullAndGroup(t *testing.T) {
	dir := t.TempDir()
	gitRun := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}
		v, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(v))
	}
	gitRun("init", "-q")
	if err := os.Mkdir(filepath.Join(dir, ".bugsnitch"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, File), bytes.Replace(policy, []byte(`"default":{"reviewDepth"`), []byte(`"default":{"owners":["maintainer"],"reviewDepth"`), 1), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".")
	gitRun("commit", "-qm", "base")
	base := gitRun("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "readme"), []byte("change"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".")
	gitRun("commit", "-qm", "candidate")
	head := gitRun("rev-parse", "HEAD")
	group := gitRun("commit-tree", gitRun("rev-parse", "HEAD^{tree}"), "-p", base, "-p", head, "-m", "group")
	s := PullSubject{"sample/shop", 21, 7, "main", base, head}
	reg := Registration{s.Repository, s.RepositoryID, s.Target, 41}
	type object = map[string]any
	groupRef := "refs/heads/gh-readonly-queue/main/pr-7-" + head
	for _, scenario := range []string{"pull", "group", "retarget", "shared reviewer", "shared CI", "missing receipt", "stale receipt", "fork", "retracted review", "wrong group ref", "extra group member", "incomplete reviews", "removed from queue", "old owner approval", "revoked owner permission"} {
		t.Run(scenario, func(t *testing.T) {
			isGroup := strings.Contains(scenario, "group") || scenario == "removed from queue"
			ev := object{"action": "synchronize", "repository": object{"id": 21, "full_name": s.Repository}, "pull_request": object{"number": 7, "head": object{"sha": head}, "base": object{"ref": "main", "sha": base}}}
			if isGroup {
				delete(ev, "pull_request")
				ev["action"] = "checks_requested"
				ev["merge_group"] = object{"base_ref": "refs/heads/main", "base_sha": base, "head_ref": groupRef, "head_sha": group}
			}
			raw, _ := json.Marshal(ev)
			var event Event
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			graphReads := 0
			transport := roundTrip(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "Bearer read-only" {
					t.Fatal("missing authentication")
				}
				var body any
				switch {
				case req.URL.Path == "/graphql" && req.Method == "POST":
					graphReads++
					target := "main"
					if scenario == "retarget" && graphReads == 2 {
						target = "other"
					}
					rid := 21
					if scenario == "fork" {
						rid = 22
					}
					approvalHead := head
					if scenario == "old owner approval" {
						approvalHead = base
					}
					owners := []any{object{"author": object{"login": "maintainer"}, "commit": object{"oid": approvalHead}, "state": "APPROVED", "authorCanPushToRepository": scenario != "revoked owner permission"}}
					pr := object{"number": 7, "state": "OPEN", "isDraft": false, "author": object{"login": "dev"}, "baseRefName": target, "baseRef": object{"target": object{"oid": base}}, "headRefOid": head,
						"headRepository": object{"databaseId": rid}, "mergeQueueEntry": object{"id": "entry"}, "reviewDecision": "APPROVED", "latestOpinionatedReviews": object{"pageInfo": object{"hasNextPage": scenario == "incomplete reviews"}, "nodes": owners}}
					if scenario == "removed from queue" {
						pr["mergeQueueEntry"] = nil
					}
					body = object{"data": object{"repository": object{"nameWithOwner": s.Repository, "databaseId": 21, "pullRequest": pr}}}
				case strings.Contains(req.URL.Path, "/git/ref/heads/") && req.Method == "GET":
					sha := group
					if scenario == "wrong group ref" {
						sha = head
					}
					body = object{"ref": groupRef, "object": object{"sha": sha, "type": "commit"}}
				case strings.HasSuffix(req.URL.Path, "/git/commits/"+group) && req.Method == "GET":
					parents := []object{{"sha": base}, {"sha": head}}
					if scenario == "extra group member" {
						parents = append(parents, object{"sha": base})
					}
					body = object{"sha": group, "parents": parents}
				case strings.HasSuffix(req.URL.Path, "/check-runs") && req.Method == "GET":
					sha := head
					if strings.Contains(req.URL.Path, group) {
						sha = group
					}
					ci := "trusted-ci"
					if scenario == "shared CI" {
						ci = "github-actions"
					}
					runs := []object{{"id": 90, "name": "build", "head_sha": sha, "status": "completed", "conclusion": "success", "app": object{"id": 17, "slug": ci}}}
					if sha == head && scenario != "missing receipt" {
						receipt, _ := ReviewRequest(s, "standard")
						if scenario == "stale receipt" {
							receipt["external_id"] = "old"
						}
						app := "independent"
						if scenario == "shared reviewer" {
							app = "github-actions"
						}
						receipt["app"] = object{"id": 41, "slug": app}
						receipt["id"] = 91
						runs = append(runs, receipt)
						if scenario == "retracted review" {
							other, _ := ReviewRequest(s, "standard")
							other["app"] = receipt["app"]
							other["id"] = 92
							other["conclusion"] = "failure"
							runs = append(runs, other)
						}
					}
					body = object{"total_count": len(runs), "check_runs": runs}
				default:
					t.Fatalf("unexpected provider operation %s %s", req.Method, req.URL.Path)
				}
				b, _ := json.Marshal(body)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: http.Header{}}, nil
			})
			d := (githubReader{&http.Client{Transport: transport}, "read-only", "https://api.github.com"}).decide(context.Background(), dir, reg, event)
			want := "hold"
			if scenario == "pull" || scenario == "group" {
				want = "would_allow"
			}
			if d.Outcome != want {
				t.Fatal(d)
			}
		})
	}
}
