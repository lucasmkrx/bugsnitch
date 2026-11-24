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

func workerBinding(ctx context.Context, a runner.Summary, turn bool) (*profile, protocol.WorkerLease, error) {
	var req protocol.WorkerLease
	key := sqlValue(a.ID)
	raw, err := journalContext(ctx, "SELECT json_object('profile',json((SELECT data FROM profile WHERE id=1)),'device',(SELECT device FROM authority WHERE attempt="+key+"),'seq',(SELECT acked FROM authority WHERE attempt="+key+"),'pending',(SELECT pending FROM authority WHERE attempt="+key+"));")
	if err != nil {
		return nil, req, runner.ErrAuthorityUnreachable
	}
	var row struct {
		Profile *profile
		Device  string
		Seq     int64
		Pending string
	}
	if err = json.Unmarshal([]byte(raw), &row); err != nil {
		return nil, req, runner.ErrAuthorityUnreachable
	}
	if row.Profile == nil {
		if row.Device != "" || a.Scope != nil {
			return nil, req, runner.ErrDeviceUnauthorized
		}
		return nil, req, nil
	}
	p := *row.Profile
	if row.Device == "" || row.Device != p.Enrollment.DeviceID || p.Enrollment.Workspace != a.Workspace {
		return nil, req, runner.ErrDeviceUnauthorized
	}
	if row.Seq < 1 || row.Pending != "" {
		return nil, req, runner.ErrAuthorityStale
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
		return nil, req, runner.ErrAuthorityUnreachable
	}
	if a.Scope != nil && (a.Scope.Device != row.Device || a.Scope.Project != project) {
		return nil, req, runner.ErrDeviceUnauthorized
	}
	if project == "" || path == "" || path != a.LocalPath {
		return nil, req, runner.ErrDeviceUnauthorized
	}
	req = protocol.WorkerLease{Scope: a.Scope, Protocol: 1, Workspace: a.Workspace, Project: project, Device: row.Device, Attempt: a.ID, Base: a.BaseSHA, Issue: a.IssueNumber, Sequence: row.Seq, SubjectVersion: a.Version, Turn: turn}
	return &p, req, nil
}

// Finished attempts have released their worker admission. Check the original
// binding and the live assignment immediately before a new publication write.
func CheckPublication(ctx context.Context, a runner.Summary) error {
	p, req, err := workerBinding(ctx, a, false)
	if err != nil || p == nil {
		return err
	}
	var live protocol.Enrollment
	if err := send(ctx, *p, "sync", protocol.Sync{Protocol: 1}, &live); err != nil {
		return err
	}
	if live.DeviceID != req.Device || live.Workspace != req.Workspace {
		return runner.ErrDeviceUnauthorized
	}
	for _, project := range live.Projects {
		if project.ID == req.Project && strings.EqualFold(project.Repository, a.Repository) {
			if project.Paused || project.PolicyVersion == 0 || (a.Scope != nil && (project.PolicyVersion != a.Scope.Policy || project.PlanID != a.Scope.Plan)) {
				return runner.ErrAuthorityStale
			}
			return nil
		}
	}
	return runner.ErrDeviceUnauthorized
}

func CheckWorker(ctx context.Context, a runner.Summary, turn bool) (time.Duration, error) {
	p, req, err := workerBinding(ctx, a, turn)
	if err != nil || p == nil {
		return 0, err
	}
	var grant protocol.WorkerGrant
	if err = send(ctx, *p, "worker-lease", req, &grant); err != nil {
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
