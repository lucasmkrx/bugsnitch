// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
	"strings"
)

// BindGeneral freezes enrollment before acquiring capacity; releases cannot
// migrate to a different device after unpairing/re-enrollment.
func BindGeneral(ws, repo, role, id string) (turns.Operation, error) {
	op := turns.Operation{Protocol: 1, ID: id, Role: role, Workspace: ws}
	raw, err := journal("SELECT data FROM profile WHERE id=1;")
	if err != nil || raw == "" {
		return op, err
	}
	var p profile
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return op, err
	}
	if ws != p.Enrollment.Workspace || p.Enrollment.DeviceID == "" {
		return op, errors.New("complete enrollment in the selected workspace")
	}
	op.Device = p.Enrollment.DeviceID
	if repo != "" {
		for _, proj := range p.Enrollment.Projects {
			if strings.EqualFold(proj.Repository, repo) {
				op.Project = proj.ID
			}
		}
		if op.Project == "" {
			return op, errors.New("manager context requires an assigned project")
		}
	}
	return op, nil
}
func SubmitGeneral(ctx context.Context, op turns.Operation) error {
	if op.Device == "" {
		return nil
	}
	p, err := getProfile()
	if err != nil {
		return err
	}
	if op.Device != p.Enrollment.DeviceID || op.Workspace != p.Enrollment.Workspace {
		return errors.New("permit belongs to the original enrollment")
	}
	var ack turns.Receipt
	if err = send(ctx, p, "model-permit", op, &ack); err != nil {
		return err
	}
	if ack.ID != op.ID || ack.Version != op.Version {
		return errors.New("general model permit receipt mismatch")
	}
	return nil
}
