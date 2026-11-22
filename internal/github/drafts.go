// SPDX-License-Identifier: MPL-2.0
package github

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

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

type DraftRepository struct{ session *Repository }

func (a *App) DraftRepository(ctx context.Context, installation, repository int64, name string) (*DraftRepository, error) {
	r, err := a.open(ctx, installation, repository, name, "draft")
	if err != nil {
		return nil, err
	}
	return &DraftRepository{r}, nil
}
func (d *DraftRepository) Close() { d.session.Close() }
func (d *DraftRepository) Check(ctx context.Context, branch, candidate, target, base string, issue int64, updated string) error {
	return d.session.call(ctx, fields{"op": "check_draft", "head": branch, "candidate": candidate, "target": target, "base": base, "number": issue, "updated": updated}, nil)
}
func (d *DraftRepository) Find(ctx context.Context, branch, target, marker string) (*DraftPull, error) {
	var out *DraftPull
	err := d.session.call(ctx, fields{"op": "find_draft", "head": branch, "target": target, "marker": marker}, &out)
	return out, err
}
func (d *DraftRepository) Create(ctx context.Context, branch, target, title, body string) (DraftPull, error) {
	var out DraftPull
	err := d.session.call(ctx, fields{"op": "create_draft", "head": branch, "target": target, "title": title, "body": body}, &out)
	return out, err
}
