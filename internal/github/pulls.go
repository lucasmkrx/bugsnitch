package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

type PullObservation struct {
	Title      string `json:"title"`
	State      string `json:"state"`
	IssueState string `json:"issueState"`
	Head       string `json:"head"`
	Base       string `json:"base"`
	Target     string `json:"target"`
	Criteria   string `json:"criteria"`
	Checks     string `json:"checks"`
	CheckCount int    `json:"checkCount"`
	Review     string `json:"review"`
	Draft      bool   `json:"draft"`
}

const inspectQuery = `query BugsnitchInspection($owner:String!,$name:String!,$pr:Int!,$issue:Int!) {
 repository(owner:$owner,name:$name) {
   nameWithOwner
   issue(number:$issue) { id number title body state }
   pullRequest(number:$pr) {
     number title state isDraft headRefOid baseRefName baseRef {target{oid}} reviewDecision
     commits(last:1){nodes{commit{oid statusCheckRollup{state contexts(first:1){totalCount}}}}}
   }
 }
}`

var oid = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func (a *App) InspectPull(ctx context.Context, install, repo int64, name string, number, issueNumber int64) (PullObservation, error) {
	var result PullObservation
	if number < 1 || number > 2147483647 || issueNumber < 1 || issueNumber > 2147483647 || number == issueNumber {
		return result, errors.New("invalid issue or pull request number")
	}
	reader, err := a.repository(ctx, install, repo, name, map[string]string{"contents": "read", "checks": "read", "issues": "read", "pull_requests": "read", "statuses": "read"})
	if err != nil {
		return result, err
	}
	owner, slug, _ := strings.Cut(name, "/")
	request := map[string]any{"query": inspectQuery, "variables": map[string]any{"owner": owner, "name": slug, "pr": number, "issue": issueNumber}}
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository struct {
				NameWithOwner string
				Issue         struct {
					ID, Title, Body, State string
					Number                 int64
				}
				PullRequest struct {
					Number                                                int64
					Title, State, HeadRefOid, BaseRefName, ReviewDecision string
					IsDraft                                               bool
					BaseRef                                               struct{ Target struct{ Oid string } }
					Commits                                               struct {
						Nodes []struct {
							Commit struct {
								Oid               string
								StatusCheckRollup *struct {
									State    string
									Contexts struct{ TotalCount int }
								}
							}
						}
					}
				}
			}
		}
	}
	if err = a.request(ctx, reader.token, "POST", "/graphql", request, &response, 200); err != nil {
		return result, err
	}
	r := response.Data.Repository
	p := r.PullRequest
	i := r.Issue
	if len(response.Errors) > 0 || !strings.EqualFold(r.NameWithOwner, name) || p.Number != number || i.Number != issueNumber || i.ID == "" || !oid.MatchString(p.HeadRefOid) || p.BaseRefName == "" {
		return result, errors.New("GitHub inspection incomplete or access denied")
	}
	criteria, _ := json.Marshal([]string{i.ID, i.Title, i.Body})
	hash := sha256.Sum256(criteria)
	result = PullObservation{Title: p.Title, State: p.State, IssueState: i.State, Head: p.HeadRefOid, Base: p.BaseRef.Target.Oid, Target: p.BaseRefName, Criteria: hex.EncodeToString(hash[:]), Checks: "UNKNOWN", Review: "UNKNOWN", Draft: p.IsDraft}
	if !oid.MatchString(result.Base) {
		result.Base = ""
	}
	if p.ReviewDecision != "" {
		result.Review = p.ReviewDecision
	}
	if nodes := p.Commits.Nodes; len(nodes) == 1 && nodes[0].Commit.Oid == result.Head {
		if rollup := nodes[0].Commit.StatusCheckRollup; rollup != nil && rollup.Contexts.TotalCount > 0 {
			result.Checks = rollup.State
			result.CheckCount = rollup.Contexts.TotalCount
		}
	}
	return result, nil
}
