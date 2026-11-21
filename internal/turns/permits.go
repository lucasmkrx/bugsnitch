// SPDX-License-Identifier: MPL-2.0
// Package turns shares capacity accounting between worker and owner-initiated
// local read-only turns. Callers must hold the workspace row lock.
package turns

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
)

type Operation struct {
	Protocol  int    `json:"protocol"`
	Workspace string `json:"workspaceId"`
	Device    string `json:"deviceId"`
	ID        string `json:"permitId"`
	Role      string `json:"role"`
	Project   string `json:"projectId,omitempty"`
	Version   int    `json:"version"`
}
type Receipt struct {
	ID      string `json:"permitId"`
	Version int    `json:"version"`
}

var identity = regexp.MustCompile(`^[a-f0-9]{32}$`)
var ErrCapacity = errors.New("active model turns occupy all available capacity")
var ErrStale = errors.New("model permit already ended")
var ErrSubject = errors.New("model permit subject mismatch")

func (a Operation) Valid() bool {
	if !identity.MatchString(a.ID) || !identity.MatchString(a.Device) || a.Workspace == "" || len(a.Workspace) > 100 || (a.Version != 1 && a.Version != 2) {
		return false
	}
	switch a.Role {
	case "manager":
		return identity.MatchString(a.Project)
	case "planner", "proof":
		return a.Project == ""
	default:
		return false
	}
}

func Available(ctx context.Context, tx *sql.Tx, ws, device string, workspaceLimit, deviceLimit int) (bool, error) {
	var all, local int
	err := tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE device_id=$2) FROM
 (SELECT device_id FROM admission WHERE workspace_id=$1 AND in_turn
 UNION ALL SELECT device_id FROM general_model_turns WHERE workspace_id=$1 AND version=1) AS occupied`, ws, device).Scan(&all, &local)
	return all < workspaceLimit && local < deviceLimit, err
}
func Apply(ctx context.Context, tx *sql.Tx, a Operation, workspaceLimit, deviceLimit int) (bool, error) {
	var v int
	var role, device, project string
	err := tx.QueryRowContext(ctx, "SELECT version,role,device_id,COALESCE(project_id,'') FROM general_model_turns WHERE workspace_id=$1 AND permit_id=$2", a.Workspace, a.ID).Scan(&v, &role, &device, &project)
	if err == nil {
		if device != a.Device || role != a.Role || project != a.Project {
			return false, ErrSubject
		}
		if v == a.Version {
			return false, nil
		}
		if v == 2 {
			return false, ErrStale
		}
		_, err = tx.ExecContext(ctx, "UPDATE general_model_turns SET version=2,ended_at=now() WHERE workspace_id=$1 AND permit_id=$2 AND version=1", a.Workspace, a.ID)
		return err == nil, err
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	if a.Version == 1 {
		ok, e := Available(ctx, tx, a.Workspace, a.Device, workspaceLimit, deviceLimit)
		if e != nil {
			return false, e
		}
		if !ok {
			return false, ErrCapacity
		}
	}
	// A release can arrive before a delayed acquisition. Its tombstone fences
	// that acquisition even when the first server response was lost.
	_, err = tx.ExecContext(ctx, `INSERT INTO general_model_turns(workspace_id,device_id,permit_id,role,project_id,version,ended_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,CASE WHEN $6=2 THEN now() END)`, a.Workspace, a.Device, a.ID, a.Role, a.Project, a.Version)
	return err == nil, err
}
