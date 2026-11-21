// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
	"strconv"
	"strings"
)

func Admit(a runner.Summary) error     { return notify(a, "admit") }
func Finish(a runner.Summary) error    { return notify(a, "finish") }
func TurnStart(a runner.Summary) error { return notify(a, "turn_start") }
func TurnEnd(a runner.Summary) error   { return notify(a, "turn_end") }

// A successfully sent request may lose its response. Advance the local cursor only after its durable acknowledgment.
func notify(a runner.Summary, op string) error {
	raw, err := journal("SELECT data FROM profile WHERE id=1;")
	if err != nil {
		return err
	}
	if raw == "" {
		return nil
	}
	var p profile
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	if p.Enrollment.Workspace != a.Workspace {
		return errors.New("local workspace does not match enrollment")
	}
	var project string
	for _, pr := range p.Enrollment.Projects {
		if strings.EqualFold(pr.Repository, a.Repository) {
			project = pr.ID
			break
		}
	}
	if project == "" {
		return errors.New("paired runner is not assigned to this repository")
	}
	name := sqlValue(a.ID)
	mapped, err := journal("SELECT path FROM mappings WHERE id=" + sqlValue(project) + ";")
	if err != nil {
		return err
	}
	if mapped == "" || ((op == "admit" || op == "turn_start") && a.LocalPath != mapped) {
		return errors.New("register the assigned local repository before execution")
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
		var row struct {
			Acked   int64
			Pending string
		}
		if err = json.Unmarshal([]byte(state), &row); err != nil {
			return err
		}
		req := protocol.Admission{Protocol: 1, CriteriaHash: a.CriteriaHash, ProjectID: project, AttemptID: a.ID, IssueNumber: a.IssueNumber, BaseSHA: a.BaseSHA, Operation: op, Sequence: row.Acked + 1}
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
			if errors.As(err, &status) && (status == 409 || status == 403 || status == 400) {
				if _, e := journal("UPDATE authority SET pending=''" + filter + ";"); e != nil {
					return e
				}
			}
			return err
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
