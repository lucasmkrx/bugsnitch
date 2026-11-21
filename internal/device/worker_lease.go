// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func CheckWorker(ctx context.Context, a runner.Summary, turn bool) (time.Duration, error) {
	key := sqlValue(a.ID)
	raw, err := journalContext(ctx, "SELECT json_object('profile',json((SELECT data FROM profile WHERE id=1)),'device',(SELECT device FROM authority WHERE attempt="+key+"),'seq',(SELECT acked FROM authority WHERE attempt="+key+"),'pending',(SELECT pending FROM authority WHERE attempt="+key+"));")
	if err != nil {
		return 0, runner.ErrAuthorityUnreachable
	}
	var row struct {
		Profile *profile
		Device  string
		Seq     int64
		Pending string
	}
	if err = json.Unmarshal([]byte(raw), &row); err != nil {
		return 0, runner.ErrAuthorityUnreachable
	}
	if row.Profile == nil {
		if row.Device != "" || a.Scope != nil {
			return 0, runner.ErrDeviceUnauthorized
		}
		return 0, nil
	}
	p := *row.Profile
	if row.Device == "" || row.Device != p.Enrollment.DeviceID || p.Enrollment.Workspace != a.Workspace {
		return 0, runner.ErrDeviceUnauthorized
	}
	if row.Seq < 1 || row.Pending != "" {
		return 0, runner.ErrAuthorityStale
	}
	var project string
	for _, pr := range p.Enrollment.Projects {
		if strings.EqualFold(pr.Repository, a.Repository) {
			project = pr.ID
			break
		}
	}
	path, err := journalContext(ctx, "SELECT path FROM mappings WHERE id="+sqlValue(project)+";")
	if err != nil {
		return 0, runner.ErrAuthorityUnreachable
	}
	if a.Scope != nil && (a.Scope.Device != row.Device || a.Scope.Project != project) {
		return 0, runner.ErrDeviceUnauthorized
	}
	if project == "" || path == "" || path != a.LocalPath {
		return 0, runner.ErrDeviceUnauthorized
	}
	req := protocol.WorkerLease{Scope: a.Scope, Protocol: 1, Workspace: a.Workspace, Project: project, Device: row.Device, Attempt: a.ID, Base: a.BaseSHA, Issue: a.IssueNumber, Sequence: row.Seq, SubjectVersion: a.Version, Turn: turn}
	var grant protocol.WorkerGrant
	if err = send(ctx, p, "worker-lease", req, &grant); err != nil {
		var status statusError
		if errors.As(err, &status) {
			switch status {
			case 401, 403:
				return 0, runner.ErrDeviceUnauthorized
			case 409, 426:
				return 0, runner.ErrAuthorityStale
			}
		}
		return 0, runner.ErrAuthorityUnreachable
	}
	if grant.Sequence != req.Sequence {
		return 0, runner.ErrAuthorityStale
	}
	if c := grant.Control; c != nil {
		if c.WorkspaceID != req.Workspace || c.ProjectID != req.Project || c.AttemptID != req.Attempt || c.DeviceID != req.Device || c.SubjectVersion != req.SubjectVersion || !time.Now().Before(c.ExpiresAt) {
			return 0, runner.ErrAuthorityStale
		}
		if _, err = runner.ApplyControl(c.ID, c.AttemptID, c.Operation, c.SubjectVersion); err != nil {
			return 0, runner.ErrAuthorityStale
		}
		return 0, runner.ErrControlPending
	}
	if grant.ValidSeconds != protocol.WorkerLeaseSeconds {
		return 0, runner.ErrAuthorityStale
	}
	return time.Duration(grant.ValidSeconds) * time.Second, nil
}
