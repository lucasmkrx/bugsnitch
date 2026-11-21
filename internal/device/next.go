// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
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
	p, err := getProfile()
	if err != nil {
		return err
	}
	var repo string
	for _, project := range p.Enrollment.Projects {
		if project.ID == *pr {
			repo = project.Repository
			break
		}
	}
	if repo == "" {
		return errors.New("the selected project is outside this device enrollment")
	}
	path, err := journal("SELECT path FROM mappings WHERE id=" + sqlValue(*pr) + ";")
	if err != nil {
		return err
	}
	if path == "" {
		return errors.New("register this project's local checkout before selecting an issue")
	}
	ctx := context.Background()
	var rec protocol.Recommendation
	if err = send(ctx, p, "next", protocol.NextRequest{Protocol: protocol.Version, Project: *pr}, &rec); err != nil {
		return err
	}
	if rec.Selection == nil {
		if rec.Reason == "" {
			return errors.New("the service returned an empty recommendation")
		}
		return errors.New(rec.Reason)
	}
	if !strings.EqualFold(repo, rec.Selection.Repository) {
		return errors.New("recommendation repository differs from enrolled project")
	}
	issue := runner.Issue(*rec.Selection)
	if err = runner.VerifySelection(ctx, p.Enrollment.Workspace, path, issue); err != nil {
		return err
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(issue)
}
