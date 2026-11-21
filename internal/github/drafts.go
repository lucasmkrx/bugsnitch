// SPDX-License-Identifier: MPL-2.0
package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type DraftRepository struct {
	repo *Repository
	name string
	id   int64
}

func (a *App) DraftRepository(ctx context.Context, installation, repository int64, name string) (*DraftRepository, error) {
	r, err := a.repository(ctx, installation, repository, name, map[string]string{"contents": "read", "issues": "read", "pull_requests": "write"})
	if err != nil {
		return nil, err
	}
	return &DraftRepository{r, name, repository}, nil
}

var branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,159}$`)

func ValidBranch(name string) bool {
	if !branchName.MatchString(name) || strings.Contains(name, "..") {
		return false
	}
	for _, s := range strings.Split(name, "/") {
		if s == "" || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") {
			return false
		}
	}
	return true
}

var ErrSubjectChanged = errors.New("GitHub issue, branch or pull request changed")

func (d DraftRepository) Check(ctx context.Context, branch, candidate, target, base string, issue int64, updated string) error {
	if !ValidBranch(branch) || !ValidBranch(target) || branch == target || candidate == base || !oid.MatchString(candidate) || !oid.MatchString(base) {
		return ErrSubjectChanged
	}
	record, err := d.repo.Get(ctx, issue)
	if err != nil {
		return err
	}
	then, e1 := time.Parse(time.RFC3339, updated)
	actual, e2 := time.Parse(time.RFC3339, record.UpdatedAt)
	if record.State != "open" || e1 != nil || e2 != nil || !actual.Equal(then) {
		return ErrSubjectChanged
	}
	for _, v := range []struct{ name, sha string }{{branch, candidate}, {target, base}} {
		var ref struct {
			Ref    string
			Object struct{ Type, SHA string }
		}
		if err = d.repo.app.request(ctx, d.repo.token, "GET", d.repo.path+"/git/ref/heads/"+v.name, nil, &ref, 200); err != nil {
			return err
		}
		if ref.Ref != "refs/heads/"+v.name || ref.Object.Type != "commit" || ref.Object.SHA != v.sha {
			return ErrSubjectChanged
		}
	}
	return nil
}

type DraftPull struct {
	Number      int64
	Body, State string
	Draft       bool
	User        struct{ Type, Login string }
	Head, Base  struct {
		Ref, Sha string
		Repo     struct{ ID int64 }
	}
}

func (d DraftRepository) Find(ctx context.Context, branch, target, marker string) (*DraftPull, error) {
	owner, _, _ := strings.Cut(d.name, "/")
	q := url.Values{"state": {"all"}, "base": {target}, "head": {owner + ":" + branch}, "per_page": {"100"}}
	var all []DraftPull
	if err := d.repo.app.request(ctx, d.repo.token, "GET", d.repo.path+"/pulls?"+q.Encode(), nil, &all, 200); err != nil {
		return nil, err
	}
	if len(all) >= 100 {
		return nil, errors.New("draft PR reconciliation exceeded page limit")
	}
	var result *DraftPull
	for _, p := range all {
		if p.Base.Repo.ID != d.id || p.Head.Repo.ID != d.id || p.Head.Ref != branch || p.Base.Ref != target {
			continue
		}
		if p.Number > 0 && p.User.Type == "Bot" && strings.EqualFold(p.User.Login, d.repo.bot) && strings.HasSuffix(p.Body, "\n"+marker) {
			if result != nil {
				return nil, ErrSubjectChanged
			}
			copy := p
			result = &copy
		} else if p.State == "open" {
			return nil, ErrSubjectChanged
		}
	}
	return result, nil
}
func (d DraftRepository) Create(ctx context.Context, branch, target, title, body string) (DraftPull, error) {
	var result DraftPull
	request := map[string]any{"title": title, "body": body, "head": branch, "base": target, "draft": true, "maintainer_can_modify": false}
	err := d.repo.app.request(ctx, d.repo.token, "POST", d.repo.path+"/pulls", request, &result, 201)
	if err == nil && (result.Number <= 0 || !result.Draft || result.Head.Repo.ID != d.id || result.Base.Repo.ID != d.id || result.Head.Ref != branch || result.Base.Ref != target) {
		return result, fmt.Errorf("GitHub draft response did not match expected repository or branches")
	}
	return result, err
}
