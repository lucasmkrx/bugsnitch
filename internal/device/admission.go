// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func TurnStart(a runner.Summary) error { return notify(a, "turn_start") }
func TurnEnd(a runner.Summary) error   { return notify(a, "turn_end") }

// A successfully sent request may lose its response. Advance the local cursor only after its durable acknowledgment.
func notify(a runner.Summary, op string) error {
	raw, err := journal("SELECT data FROM profile WHERE id=1;")
	if err != nil {
		return err
	}
	if raw == "" {
		bound, e := journal("SELECT device FROM authority WHERE attempt=" + sqlValue(a.ID) + ";")
		if e != nil {
			return e
		}
		if bound != "" || a.Scope != nil {
			return runner.ErrDeviceUnauthorized
		}
		return nil
	}
	var p profile
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	if p.Enrollment.Workspace != a.Workspace {
		return runner.ErrDeviceUnauthorized
	}
	var project string
	for _, pr := range p.Enrollment.Projects {
		if strings.EqualFold(pr.Repository, a.Repository) {
			project = pr.ID
			break
		}
	}
	if a.Scope != nil && (a.Scope.Device != p.Enrollment.DeviceID || a.Scope.Project != project) {
		return runner.ErrDeviceUnauthorized
	}
	if project == "" {
		return runner.ErrDeviceUnauthorized
	}
	name := sqlValue(a.ID)
	mapped, err := journal("SELECT path FROM mappings WHERE id=" + sqlValue(project) + ";")
	if err != nil {
		return err
	}
	if mapped == "" || ((op == "admit" || op == "turn_start") && a.LocalPath != mapped) {
		return errors.Join(runner.ErrDeviceUnauthorized, errors.New("register the assigned local repository before execution"))
	}
	_, err = journal("INSERT OR IGNORE INTO authority(attempt,device) VALUES(" + name + "," + sqlValue(p.Enrollment.DeviceID) + ");")
	if err != nil {
		return err
	}
	for {
		state, err := journal("SELECT json_object('acked',acked,'pending',pending) FROM authority WHERE attempt=" + name + " AND device=" + sqlValue(p.Enrollment.DeviceID) + ";")
		if err != nil {
			return err
		}
		if state == "" {
			return runner.ErrDeviceUnauthorized
		}
		var row struct {
			Acked   int64
			Pending string
		}
		if err = json.Unmarshal([]byte(state), &row); err != nil {
			return err
		}
		req := protocol.Admission{Scope: a.Scope, Protocol: 1, CriteriaHash: a.CriteriaHash, ProjectID: project, AttemptID: a.ID, IssueNumber: a.IssueNumber, BaseSHA: a.BaseSHA, Operation: op, Sequence: row.Acked + 1}
		if op == "finish" {
			req.State = a.State
		}
		if row.Pending == "" {
			b, err := json.Marshal(req)
			if err != nil {
				return err
			}
			n, err := journal("UPDATE authority SET pending=" + sqlValue(string(b)) + " WHERE attempt=" + name + " AND pending='' AND acked=" + strconv.FormatInt(row.Acked, 10) + "; SELECT changes();")
			if err != nil {
				return err
			}
			if n != "1" {
				continue
			}
			row.Pending = string(b)
		} else if err = json.Unmarshal([]byte(row.Pending), &req); err != nil {
			return err
		}
		var ack struct{ Sequence int64 }
		err = send(context.Background(), p, "admission", req, &ack)
		filter := " WHERE attempt=" + name + " AND pending=" + sqlValue(row.Pending)
		if err != nil {
			var status statusError
			errors.As(err, &status)
			if req.Operation != "turn_end" && req.Operation != "finish" && (status == 409 || status == 403 || status == 400) {
				if _, e := journal("UPDATE authority SET pending=''" + filter + ";"); e != nil {
					return e
				}
			}
			switch status {
			case 401, 403:
				return errors.Join(runner.ErrDeviceUnauthorized, err)
			case 400, 409, 426:
				return errors.Join(runner.ErrAuthorityStale, err)
			default:
				return errors.Join(runner.ErrAuthorityUnreachable, err)
			}
		}
		if ack.Sequence != req.Sequence {
			return errors.New("invalid admission acknowledgment")
		}
		if _, err = journal("UPDATE authority SET acked=" + strconv.FormatInt(req.Sequence, 10) + ",pending=''" + filter + ";"); err != nil {
			return err
		}
		if req.Operation == op {
			return nil
		}
	}
}
