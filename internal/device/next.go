// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func next(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	pr := fs.String("project", "", "assigned project ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pr == "" || fs.NArg() != 0 {
		return errors.New("use next --project PROJECT_ID")
	}
	selector, err := selectProject(*pr)
	if err != nil {
		return err
	}
	rec, err := selector.recommend(context.Background(), false)
	if err != nil {
		return err
	}
	if rec.Selection == nil {
		return errors.New(rec.Reason)
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(rec.Selection)
}

type selectionProject struct {
	enrolled       profile
	id, repo, path string
	legacyNext     bool
}

func selectProject(projectID string) (selectionProject, error) {
	var selected selectionProject
	p, err := getProfile()
	if err != nil {
		return selected, err
	}
	selected.enrolled = p
	selected.id = projectID
	for _, project := range p.Enrollment.Projects {
		if project.ID == projectID {
			selected.repo = project.Repository
			break
		}
	}
	if selected.repo == "" {
		return selected, errors.New("the selected project is outside this device enrollment")
	}
	selected.path, err = journal("SELECT path FROM mappings WHERE id=" + sqlValue(projectID) + ";")
	if err != nil {
		return selected, err
	}
	if selected.path == "" {
		return selected, errors.New("register this project's local checkout before selecting an issue")
	}
	return selected, nil
}

func (p *selectionProject) recommend(ctx context.Context, waitForTurn bool) (protocol.Recommendation, error) {
	var rec protocol.Recommendation
	req := protocol.NextRequest{Protocol: protocol.Version, Project: p.id, WaitForTurn: waitForTurn && !p.legacyNext}
	err := send(ctx, p.enrolled, "next", req, &rec)
	if req.WaitForTurn && errors.Is(err, statusError(http.StatusBadRequest)) {
		// Older coordinators reject unknown fields. Selection has no side
		// effects, so retry once with the original request format.
		req.WaitForTurn = false
		err = send(ctx, p.enrolled, "next", req, &rec)
		if err == nil {
			p.legacyNext = true
		}
	}
	if err != nil {
		return rec, err
	}
	if rec.Selection == nil {
		if rec.Reason == "" {
			return rec, errors.New("the service returned an empty recommendation")
		}
		return rec, nil
	}
	if !strings.EqualFold(p.repo, rec.Selection.Repository) {
		return rec, errors.New("recommendation repository differs from enrolled project")
	}
	err = runner.VerifySelection(ctx, p.enrolled.Enrollment.Workspace, p.path, runner.Issue(*rec.Selection), waitForTurn)
	return rec, err
}
