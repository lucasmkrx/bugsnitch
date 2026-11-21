// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// VerifySelection checks the same local checkout and canonical issue used by Start.
// The preflight cannot grant a model slot or reserve a ticket.
func VerifySelection(ctx context.Context, w, path string, i Issue) error {
	if err := i.verify(); err != nil {
		return err
	}
	if i.GitHubID < 1 {
		return errors.New("recommendation lacks canonical GitHub identity")
	}
	p, err := project(w, i.Repository)
	if err != nil {
		return err
	}
	if p.Path != path {
		return errors.New("the paired repository mapping differs from the worker checkout")
	}
	if err = checkOrigin(path, i.Repository); err != nil {
		return err
	}
	b, err := query(fmt.Sprintf("SELECT CASE WHEN EXISTS(SELECT 1 FROM journal_attempts WHERE workspace=%s AND repo=%s AND issue=%d) THEN 'prior' WHEN (SELECT count(*) FROM journal_attempts WHERE workspace=%s AND json_extract(data,'$.state')='running')>=2 THEN 'capacity' ELSE '' END;", literal(w), literal(strings.ToLower(i.Repository)), i.Number, literal(w)))
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(b)) {
	case "prior":
		return errors.New("this issue has a local attempt; inspect or explicitly retry it")
	case "capacity":
		return errors.New("both local implementation slots are reserved")
	}
	exists, err := query("SELECT 1 FROM sqlite_master WHERE type='table' AND name='model_permits';")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(exists)) == "1" {
		b, err = query("SELECT count(*)>=2 FROM model_permits;")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) == "1" {
			return errModelCapacity
		}
	}
	return readGitHubIssue(ctx, i)
}
