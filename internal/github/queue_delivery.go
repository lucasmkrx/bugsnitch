// SPDX-License-Identifier: MPL-2.0
package github

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
)

type QueueSubject struct {
	Head, Base, Target, Label    string
	InQueue, Guarded, CanEnqueue bool
}

func (a *App) QueueEnabled(repo int64) bool {
	for _, id := range strings.Split(os.Getenv("BUGSNITCH_GITHUB_QUEUE_REPOSITORIES"), ",") {
		if strings.TrimSpace(id) == strconv.FormatInt(repo, 10) && repo > 0 {
			return true
		}
	}
	return false
}
func (a *App) QueueRepository(ctx context.Context, install, repo int64, name string, write bool) (*Repository, error) {
	if !a.QueueEnabled(repo) {
		return nil, errors.New("repository not configured for queue protection inspection")
	}
	scope := "queue_read"
	if write {

		scope = "queue"
	}
	return a.open(ctx, install, repo, name, scope)
}

func (r *Repository) QueueSubject(ctx context.Context, n int64, target string) (QueueSubject, error) {
	var out QueueSubject
	err := r.call(ctx, fields{"op": "queue_subject", "number": n, "target": target}, &out)
	return out, err
}

func (r *Repository) GroupMatches(ctx context.Context, group, ref, base, head string) (bool, error) {
	var match bool
	err := r.call(ctx, fields{"op": "queue_group", "group": group, "group_ref": ref, "base": base, "head": head}, &match)
	return match, err
}
func (r *Repository) QueueCheck(ctx context.Context, sha, request, label string) error {
	return r.call(ctx, fields{"op": "queue_check", "label": label, "sha": sha, "request": request}, nil)
}
func (r *Repository) Enqueue(ctx context.Context, n int64, head, base, target string) error {
	return r.call(ctx, fields{"op": "enqueue", "number": n, "head": head, "base": base, "target": target}, nil)
}
