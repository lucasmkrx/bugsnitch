// SPDX-License-Identifier: MPL-2.0
package approvalpolicy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type PullSubject struct {
	Repository   string `json:"repository"`
	RepositoryID int64  `json:"repositoryId"`
	PR           int    `json:"pr"`
	Target       string `json:"target"`
	Base         string `json:"base"`
	Head         string `json:"head"`
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var groupSuffix = regexp.MustCompile(`^[a-f0-9]+$`)
var targetName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]{0,200}$`)

const reviewPrefix = "Bugsnitch independent review / "

func (s PullSubject) valid() bool {
	return repoName.MatchString(s.Repository) && s.RepositoryID > 0 && s.PR > 0 && targetName.MatchString(s.Target) &&
		!strings.Contains(s.Target, "..") && revision.MatchString(s.Base) && revision.MatchString(s.Head) && s.Head != s.Base
}

// ReviewRequest produces a GitHub Check Run body for a trusted reviewer App,
// after that service has actually reviewed this subject at the stated depth.
func ReviewRequest(s PullSubject, depth string) (map[string]any, error) {
	if !s.valid() || depths[depth] == 0 {
		return nil, errors.New("invalid review subject or depth")
	}
	msg := strings.Join([]string{"bugsnitch-review-v1", strings.ToLower(s.Repository), strconv.FormatInt(s.RepositoryID, 10),
		strconv.Itoa(s.PR), s.Target, s.Base, s.Head, depth}, "\n")
	id := fmt.Sprintf("bs-review-v1-%x", sha256.Sum256([]byte(msg)))
	return map[string]any{"name": reviewPrefix + depth, "head_sha": s.Head, "status": "completed", "conclusion": "success", "external_id": id}, nil
}

type Event struct {
	Action     string `json:"action"`
	Repository struct {
		ID   int64
		Name string `json:"full_name"`
	} `json:"repository"`
	Pull *struct {
		Number     int
		Base, Head struct{ SHA, Ref string }
	} `json:"pull_request"`
	Group *struct {
		Base   string `json:"base_sha"`
		SHA    string `json:"head_sha"`
		Target string `json:"base_ref"`
		Ref    string `json:"head_ref"`
	} `json:"merge_group"`
}

type Registration struct {
	Repository   string
	RepositoryID int64
	Target       string
	ReviewApp    int64
}

type observation struct {
	Pull                             PullSubject
	Author, Reviewer, Depth, Receipt string
	Checks                           []CheckResult
	Owners                           []string
	Tested                           string
}

type githubReader struct {
	client *http.Client
	token  string
	base   string
}

func (g githubReader) read(ctx context.Context, api string, payload, dest any) error {
	method := http.MethodGet
	var body io.Reader
	if payload != nil {
		if api != "/graphql" {
			return errors.New("unsupported approval observation")
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		method, body = http.MethodPost, bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+api, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return errors.New("GitHub approval observation unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub approval observation returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(b) > 2<<20 || json.Unmarshal(b, dest) != nil {
		return errors.New("invalid or oversized GitHub observation")
	}
	return nil
}

type checkRun struct {
	ID                       int64
	Name, Status, Conclusion string
	Head                     string `json:"head_sha"`
	External                 string `json:"external_id"`
	App                      struct {
		ID   int64
		Slug string
	}
}

func (g githubReader) checks(ctx context.Context, source string, sha string) ([]checkRun, error) {
	var result struct {
		Total *int       `json:"total_count"`
		Runs  []checkRun `json:"check_runs"`
	}
	err := g.read(ctx, source+"/commits/"+sha+"/check-runs?filter=latest&per_page=100", nil, &result)
	if err != nil {
		return nil, err
	}
	if result.Total == nil || *result.Total != len(result.Runs) || len(result.Runs) > 100 {
		return nil, errors.New("incomplete check collection")
	}
	for _, c := range result.Runs {
		if c.Head != sha || c.ID < 1 || c.App.ID < 1 {
			return nil, errors.New("check observation has an unexpected revision or producer")
		}
	}
	return result.Runs, nil
}

const pullQuery = `query ApprovalSubject($owner:String!,$name:String!,$pr:Int!){repository(owner:$owner,name:$name){databaseId nameWithOwner pullRequest(number:$pr){
 number state isDraft author{login} baseRefName baseRef{target{oid}} headRefOid headRepository{databaseId} mergeQueueEntry{id} reviewDecision
 latestOpinionatedReviews(first:100){pageInfo{hasNextPage} nodes{state author{login} authorCanPushToRepository commit{oid}}}
}}}`

type pullObservation struct {
	Number                         int
	State, BaseRefName, HeadRefOID string
	IsDraft                        *bool
	Author                         struct{ Login string }
	BaseRef                        struct{ Target struct{ OID string } }
	HeadRepository                 struct{ DatabaseID int64 }
	MergeQueueEntry                *struct{ ID string }
	ReviewDecision                 json.RawMessage
	LatestOpinionatedReviews       *struct {
		PageInfo *struct{ HasNextPage bool }
		Nodes    []struct {
			State                     string
			Author                    struct{ Login string }
			AuthorCanPushToRepository bool
			Commit                    struct{ OID string }
		}
	}
}

func (g githubReader) observe(ctx context.Context, r Registration, event Event) (observation, error) {
	var out observation
	if !repoName.MatchString(r.Repository) || !targetName.MatchString(r.Target) || strings.Contains(r.Target, "..") || r.RepositoryID < 1 || r.ReviewApp < 1 ||
		event.Repository.ID != r.RepositoryID || !strings.EqualFold(event.Repository.Name, r.Repository) || (event.Pull == nil) == (event.Group == nil) {
		return out, errors.New("event does not match the enrolled repository or delivery type")
	}
	source := "/repos/" + r.Repository
	s := PullSubject{Repository: r.Repository, RepositoryID: r.RepositoryID, Target: r.Target}
	if event.Pull != nil {
		s.PR, s.Base, s.Head = event.Pull.Number, event.Pull.Base.SHA, event.Pull.Head.SHA
		if event.Pull.Base.Ref != r.Target || !s.valid() {
			return out, errors.New("unexpected PR event subject")
		}
		out.Tested = s.Head
	} else {
		group := event.Group
		if event.Action != "checks_requested" || group.Target != "refs/heads/"+r.Target || !revision.MatchString(group.Base) || !revision.MatchString(group.SHA) {
			return out, errors.New("unsupported merge group")
		}
		prefix := "refs/heads/gh-readonly-queue/" + r.Target + "/pr-"
		suffix, ok := strings.CutPrefix(group.Ref, prefix)
		number, tail, okTail := strings.Cut(suffix, "-")
		n, err := strconv.Atoi(number)
		if !ok || !okTail || err != nil || n < 1 || len(tail) > 80 || !groupSuffix.MatchString(tail) {
			return out, errors.New("unsupported merge-group ref format")
		}
		var ref struct {
			Ref    string
			Object struct{ Type, SHA string }
		}
		if err := g.read(ctx, source+"/git/ref/heads/"+url.PathEscape(strings.TrimPrefix(group.Ref, "refs/heads/")), nil, &ref); err != nil {
			return out, err
		}
		var commit struct {
			SHA     string
			Parents []struct{ SHA string }
		}
		if err := g.read(ctx, source+"/git/commits/"+group.SHA, nil, &commit); err != nil {
			return out, err
		}
		if ref.Ref != group.Ref || ref.Object.Type != "commit" || ref.Object.SHA != group.SHA || commit.SHA != group.SHA || len(commit.Parents) != 2 || commit.Parents[0].SHA != group.Base {
			return out, errors.New("merge group moved or contains unsupported membership")
		}
		s.PR, s.Base, s.Head = n, group.Base, commit.Parents[1].SHA
		if !s.valid() || s.Head == group.SHA {
			return out, errors.New("invalid merge group parent")
		}
		out.Tested = group.SHA
	}
	owner, name, _ := strings.Cut(s.Repository, "/")
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository struct {
				DatabaseID    int64
				NameWithOwner string
				PullRequest   *pullObservation
			}
		}
	}
	payload := map[string]any{"query": pullQuery, "variables": map[string]any{"owner": owner, "name": name, "pr": s.PR}}
	if err := g.read(ctx, "/graphql", payload, &response); err != nil {
		return out, err
	}
	repo := response.Data.Repository
	p := repo.PullRequest
	if len(response.Errors) > 0 || repo.DatabaseID != r.RepositoryID || !strings.EqualFold(repo.NameWithOwner, r.Repository) ||
		p == nil || p.State != "OPEN" || (p.IsDraft == nil || *p.IsDraft) || p.Number != s.PR || p.BaseRefName != s.Target || p.BaseRef.Target.OID != s.Base || p.HeadRefOID != s.Head ||
		p.HeadRepository.DatabaseID != s.RepositoryID || p.Author.Login == "" {
		return out, errors.New("PR revision, destination, repository or open status changed")
	}
	if event.Group != nil && (p.MergeQueueEntry == nil || p.MergeQueueEntry.ID == "") {
		return out, errors.New("PR left the merge queue")
	}
	if string(p.ReviewDecision) != "null" && string(p.ReviewDecision) != `"APPROVED"` {
		return out, errors.New("GitHub review decision holds the PR")
	}
	reviews := p.LatestOpinionatedReviews
	if reviews == nil || reviews.PageInfo == nil || reviews.PageInfo.HasNextPage {
		return out, errors.New("incomplete latest review collection")
	}
	for _, review := range reviews.Nodes {
		if review.AuthorCanPushToRepository && review.State == "CHANGES_REQUESTED" {
			return out, errors.New("changes requested on PR")
		}
		if review.AuthorCanPushToRepository && review.State == "APPROVED" && review.Commit.OID == s.Head {
			out.Owners = append(out.Owners, review.Author.Login)
		}
	}
	prRuns, err := g.checks(ctx, source, s.Head)
	if err != nil {
		return out, err
	}
	seenReviews := map[string]bool{}
	for _, c := range prRuns {
		if c.App.ID != r.ReviewApp || c.App.Slug == "" || c.App.Slug == "github-actions" || !strings.HasPrefix(c.Name, reviewPrefix) {
			continue
		}
		level := strings.TrimPrefix(c.Name, reviewPrefix)
		want, err := ReviewRequest(s, level)
		if err != nil || c.External != want["external_id"] {
			continue
		}
		if seenReviews[level] || c.Status != "completed" || c.Conclusion != "success" {
			return out, errors.New("ambiguous or unsuccessful independent review receipt")
		}
		seenReviews[level] = true
		if depths[level] > depths[out.Depth] {
			out.Depth, out.Reviewer = level, c.App.Slug+"[bot]"
			out.Receipt = fmt.Sprintf("github:%d/check-runs/%d", r.RepositoryID, c.ID)
		}
	}
	if out.Receipt == "" {
		return out, errors.New("missing trusted independent review receipt for this exact PR, branch, base and head")
	}
	runs := prRuns
	if event.Group != nil {
		runs, err = g.checks(ctx, source, out.Tested)
		if err != nil {
			return out, err
		}
	}
	for _, c := range runs {
		// A shared Actions App can run workflows editable on the candidate. This
		// adapter accepts dedicated producers configured by the trusted policy.
		if c.App.ID == r.ReviewApp || c.App.Slug == "" || c.App.Slug == "github-actions" {
			continue
		}
		state := c.Conclusion
		if c.Status != "completed" {
			state = "pending"
		}
		out.Checks = append(out.Checks, CheckResult{Check{c.Name, c.App.ID}, state})
	}
	out.Pull, out.Author = s, p.Author.Login
	return out, nil
}

// ObserveGitHub only reads github.com using an authenticated, read-scoped token.
func ObserveGitHub(ctx context.Context, repo string, r Registration, event Event, token string) Decision {
	if token == "" {
		return Decision{Outcome: "hold", Reasons: []string{"provide a read-scoped GitHub token"}}
	}
	g := githubReader{&http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, token, "https://api.github.com"}
	return g.decide(ctx, repo, r, event)
}

func (g githubReader) decide(ctx context.Context, repo string, r Registration, event Event) Decision {
	o, err := g.observe(ctx, r, event)
	if err != nil {
		return Decision{Outcome: "hold", Reasons: []string{err.Error()}}
	}
	s := o.Pull
	if o.Tested != s.Head {
		// The review and human approvals cover the exact PR tree. Only carry
		// them across to a group with byte-identical content and one member.
		if _, err = git(ctx, repo, 1, "diff", "--quiet", "--no-ext-diff", "--no-textconv", s.Head, o.Tested, "--"); err != nil {
			return Decision{Outcome: "hold", Reasons: []string{"combined group requires its own independent review"}}
		}
	}
	e := Evidence{Base: s.Base, Head: o.Tested, Author: o.Author, Reviewer: o.Reviewer, ReviewDepth: o.Depth, ReviewConclusion: "pass",
		Reference: o.Receipt, Checks: o.Checks, Approvers: o.Owners}
	d := Inspect(ctx, repo, s.Base, o.Tested, e)
	if d.Outcome == "would_allow" {
		fresh, err := g.observe(ctx, r, event)
		if err != nil || !reflect.DeepEqual(o, fresh) {
			d.Outcome = "hold"
			d.Reasons = []string{"GitHub subject, reviews or checks changed during evaluation"}
		}
	}
	return d
}
