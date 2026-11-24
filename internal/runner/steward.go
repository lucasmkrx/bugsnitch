// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

// The previous attempt remains terminal. Each correction takes a new local and
// hosted admission, with an immutable link to the PR head it may fast-forward.
type StewardSubject struct {
	SignalDigest string   `json:"signalDigest,omitempty"`
	MergeCommit  string   `json:"mergeCommit,omitempty"`
	Conflicts    []string `json:"conflicts,omitempty"`
	Pull         int      `json:"pull"`
	RootAttempt  string   `json:"rootAttempt"`
	Target       string   `json:"target"`
	OriginalBase string   `json:"originalBase"`
	PreviousHead string   `json:"previousHead"`
	Findings     []string `json:"findings"`
}

type stewardPR struct {
	Number                                                                             int
	HeadRefOid, BaseRefOid, BaseRefName, HeadRefName, State, ReviewDecision, Mergeable string
	HeadRepository                                                                     struct{ NameWithOwner string }
	MergeQueueEntry                                                                    *struct{ ID string }
	IsDraft                                                                            bool
	ReviewThreads                                                                      struct {
		Nodes []struct {
			IsResolved, IsOutdated bool
			Comments               struct{ Nodes []struct{ BodyText string } }
		}
	}
	Commits struct {
		Nodes []struct {
			Commit struct {
				Oid               string
				StatusCheckRollup struct {
					Contexts struct {
						Nodes []struct{ ID, Name, Context, Conclusion, State string }
					}
				}
			}
		}
	}
}

const stewardQuery = `query BugsnitchSteward($owner:String!,$repo:String!,$n:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$n){
 number state isDraft headRefOid headRefName headRepository{nameWithOwner} baseRefName baseRefOid mergeQueueEntry{id} mergeable reviewDecision
 reviewThreads(first:20){nodes{isResolved isOutdated comments(last:1){nodes{bodyText}}}}
 commits(last:1){nodes{commit{oid statusCheckRollup{contexts(first:50){nodes{... on CheckRun{id name conclusion} ... on StatusContext{id context state}}}}}}}
}}}`

func githubCLI(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", append([]string{"api", "--hostname", "github.com"}, args...)...)
	cmd.Dir = os.TempDir()
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GH_PROMPT_DISABLED=1", "GH_HOST=github.com", "NO_COLOR=1"}
	for _, key := range []string{"HOME", "GH_CONFIG_DIR", "GITHUB_TOKEN", "GH_TOKEN", "XDG_CONFIG_HOME"} {
		if v, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	out := &capped{max: 1 << 20}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil || out.overflow {
		return nil, errors.New("GitHub operation unavailable; check gh authentication and repository access")
	}
	return out.buffer.Bytes(), nil
}

var ErrStewardIdle = errors.New("no failing check or unresolved feedback on the inspected PR")
var ErrStewardFinished = errors.New("PR closed, received approval or entered a merge queue")

func readStewardPR(ctx context.Context, repo string, n int) (stewardPR, error) {
	var pr stewardPR
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !repoPattern.MatchString(repo) || n < 1 {
		return pr, errors.New("invalid steward pull request")
	}
	raw, err := githubCLI(ctx, "graphql", "-f", "query="+stewardQuery, "-f", "owner="+owner, "-f", "repo="+name, "-F", "n="+strconv.Itoa(n))
	if err != nil {
		return pr, err
	}
	var data struct {
		Errors []json.RawMessage
		Data   struct {
			Repository struct{ PullRequest stewardPR }
		}
	}
	if json.Unmarshal(raw, &data) != nil || len(data.Errors) > 0 || data.Data.Repository.PullRequest.Number != n {
		return pr, errors.New("GitHub returned an incomplete pull request")
	}
	return data.Data.Repository.PullRequest, nil
}

func (p stewardPR) matches(repo, branch, head, target string) bool {
	return p.State == "OPEN" && p.MergeQueueEntry == nil && p.HeadRefName == branch &&
		strings.EqualFold(p.HeadRepository.NameWithOwner, repo) && p.HeadRefOid == head && p.BaseRefName == target
}
func (p stewardPR) findings() []string {
	var findings []string
	add := func(label, msg string) {
		msg = strings.TrimSpace(incidents.Redact(msg))
		runes := []rune(msg)
		if len(runes) > 400 {
			msg = string(runes[:400])
		}
		if msg != "" && len(findings) < 16 {
			findings = append(findings, label+msg)
		}
	}
	for _, node := range p.Commits.Nodes {
		if node.Commit.Oid != p.HeadRefOid {
			continue
		}
		for _, c := range node.Commit.StatusCheckRollup.Contexts.Nodes {
			switch c.Conclusion {
			case "FAILURE", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
				add("Failed check: ", c.Name)
			}
			if c.State == "FAILURE" || c.State == "ERROR" {
				add("Failed status: ", c.Context)
			}
		}
	}
	for _, t := range p.ReviewThreads.Nodes {
		if !t.IsOutdated && !t.IsResolved && len(t.Comments.Nodes) == 1 {
			add("Unresolved review: ", t.Comments.Nodes[0].BodyText)
		}
	}
	return findings
}

func Steward(ctx context.Context, id string, version int, candidate string, n int) (Attempt, error) {
	prior, err := Get(id)
	if err != nil {
		return prior, err
	}
	if prior.State != "completed" || prior.Phase != "wrap_up" || prior.Version != version || candidate == "" || prior.CandidateSHA != candidate || n < 1 {
		return prior, errors.New("select the exact completed attempt, candidate and pull request")
	}
	if err = prior.cleanAt(candidate); err != nil {
		return prior, err
	}
	branch := "bugsnitch/" + prior.ID
	sub := StewardSubject{Pull: n, RootAttempt: prior.ID, OriginalBase: prior.BaseSHA, PreviousHead: candidate}
	if old := prior.Steward; old != nil {
		if old.Pull != n {
			return prior, errors.New("steward correction belongs to a different pull request")
		}
		sub.RootAttempt = old.RootAttempt
		sub.OriginalBase = old.OriginalBase
		sub.Target = old.Target
		branch = "bugsnitch/" + old.RootAttempt
	}
	pr, err := readStewardPR(ctx, prior.Repository, n)
	if err != nil {
		return prior, err
	}
	if sub.Target == "" {
		sub.Target = pr.BaseRefName
	}
	if pr.State != "OPEN" || pr.MergeQueueEntry != nil || pr.ReviewDecision == "APPROVED" {
		return prior, ErrStewardFinished
	}
	if !pr.matches(prior.Repository, branch, candidate, sub.Target) {
		return prior, errors.New("PR moved, closed, entered its queue, or received approval")
	}
	sub.Findings = pr.findings()
	if pr.Mergeable == "CONFLICTING" {
		configured, err := project(prior.Workspace, prior.Repository)
		if err != nil {
			return prior, err
		}
		merge, paths, err := stewardMerge(ctx, prior, configured.Config, pr.BaseRefOid)
		if err != nil {
			return prior, err
		}
		sub.MergeCommit, sub.Conflicts, sub.OriginalBase = merge, paths, pr.BaseRefOid
		sub.Findings = append(sub.Findings, "Resolve each recorded text conflict against the new target while preserving the issue criteria. The registered files contain both sides.")
	}
	signals := append([]string{}, sub.Findings...)
	for _, node := range pr.Commits.Nodes {
		if node.Commit.Oid != pr.HeadRefOid {
			continue
		}
		for _, check := range node.Commit.StatusCheckRollup.Contexts.Nodes {
			switch check.Conclusion + check.State {
			case "FAILURE", "ERROR", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
				signals = append(signals, check.ID)
			}
		}
	}
	if len(sub.Conflicts) > 0 {
		signals = append(signals, sub.OriginalBase)
	}
	slices.Sort(signals)
	raw, _ := json.Marshal(signals)
	sub.SignalDigest = fmt.Sprintf("%x", sha256.Sum256(raw))
	if prior.Steward != nil && prior.Steward.SignalDigest == sub.SignalDigest {
		return prior, ErrStewardIdle
	}
	if len(sub.Findings) == 0 {
		return prior, ErrStewardIdle
	}
	return create(ctx, prior.Workspace, prior.Repository, prior.Issue, prior.ID, fmt.Sprintf("Repair PR #%d", n), prior.LocalRunScope, nil, &sub)
}
