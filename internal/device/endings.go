// SPDX-License-Identifier: MPL-2.0
package device

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

const endingSchema = "CREATE TABLE IF NOT EXISTS worker_endings(attempt TEXT PRIMARY KEY,device TEXT NOT NULL,version INTEGER NOT NULL,subject TEXT NOT NULL); "

// Finish is called after the actual local child has returned. Persist that
// observation before sending it, including when an earlier turn_end lost its ack.
func Finish(a runner.Summary) error {
	key := sqlValue(a.ID)
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = journal(endingSchema + "INSERT INTO worker_endings SELECT attempt,device," + strconv.Itoa(a.Version) + "," + sqlValue(string(b)) + " FROM authority WHERE attempt=" + key + " ON CONFLICT(attempt) DO UPDATE SET subject=excluded.subject,version=excluded.version WHERE worker_endings.device=excluded.device AND worker_endings.version<=excluded.version;")
	if err != nil {
		return err
	}
	return completeEnding(a.ID)
}
func Admit(a runner.Summary) error {
	if err := completeEnding(a.ID); err != nil {
		return err
	}
	return notify(a, "admit")
}
func completeEnding(id string) error {
	key := sqlValue(id)
	raw, err := journal(endingSchema + "SELECT json_object('device',w.device,'subject',json(w.subject),'pending',a.pending) FROM worker_endings w JOIN authority a ON w.attempt=a.attempt AND w.device=a.device WHERE w.attempt=" + key + ";")
	if err != nil || raw == "" {
		return err
	}
	var rec struct {
		Device, Pending string
		Subject         runner.Summary
	}
	if err = json.Unmarshal([]byte(raw), &rec); err != nil {
		return err
	}
	p, err := getProfile()
	if err != nil || p.Enrollment.DeviceID != rec.Device {
		return runner.ErrDeviceUnauthorized
	}
	if rec.Pending != "" {
		var op protocol.Admission
		if err = json.Unmarshal([]byte(rec.Pending), &op); err != nil {
			return err
		}
		switch op.Operation {
		case "turn_start":
			// Finish records quiescence under the owning attempt lock. The
			// start may have committed remotely. Fence its exact sequence
			// without requesting another model grant, even after a restart.
			op.Operation = "turn_abandon"
			op.Sequence++
			body, err := json.Marshal(op)
			if err != nil {
				return err
			}
			n, err := journal("UPDATE authority SET pending=" + sqlValue(string(body)) + " WHERE attempt=" + key + " AND device=" + sqlValue(rec.Device) + " AND pending=" + sqlValue(rec.Pending) + "; SELECT changes();")
			if err != nil {
				return err
			}
			if n != "1" {
				return errors.New("worker admission changed while reconciling its ending")
			}
		case "turn_abandon", "turn_end", "finish":
		default:
			return errors.New("unconfirmed worker start requires reconciliation on the original computer")
		}
	}
	if err = notify(rec.Subject, "finish"); err != nil {
		return err
	}
	_, err = journal("DELETE FROM worker_endings WHERE attempt=" + key + " AND device=" + sqlValue(rec.Device) + " AND version=" + strconv.Itoa(rec.Subject.Version) + ";")
	return err
}
func flushEndings(device string) error {
	raw, err := journal(endingSchema + "SELECT json_group_array(json_object('id',attempt,'version',version)) FROM worker_endings WHERE device=" + sqlValue(device) + ";")
	if err != nil {
		return err
	}
	var rows []struct {
		ID      string
		Version int
	}
	if err = json.Unmarshal([]byte(raw), &rows); err != nil {
		return err
	}
	var failures []error
	for _, r := range rows {
		if err = runner.ReplayEnded(r.ID, r.Version, completeEnding); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
