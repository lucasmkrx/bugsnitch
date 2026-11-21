// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var commandPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func RequestControl(id, op string) (string, error) {
	a, err := Get(id)
	if err != nil {
		return "", err
	}
	return ApplyControl(fmt.Sprintf("local-%s-%s-%d", id, op, a.Version), id, op, a.Version)
}

// ApplyControl durably binds each command to the exact local subject revision.
// A running process owns completion. Receipt alone never proves it stopped.
func ApplyControl(command, id, op string, version int) (string, error) {
	if !commandPattern.MatchString(command) || !idPattern.MatchString(id) || version < 1 || (op != "stop" && op != "pause") {
		return "", errors.New("invalid attempt control")
	}
	key, attempt, operation := literal(command), literal(id), literal(op)
	var target = "waiting"
	if op == "stop" {
		target = "cancelled"
	}
	b, err := query(fmt.Sprintf(`BEGIN IMMEDIATE;
 INSERT OR IGNORE INTO journal_controls(command_id,attempt_id,op,subject_version,status)
 SELECT %[1]s,id,%[3]s,%[4]d,CASE WHEN version<>%[4]d THEN 'unavailable' WHEN json_extract(data,'$.state')='running' THEN 'received' WHEN json_extract(data,'$.state')='waiting' THEN 'apply_waiting' ELSE 'unavailable' END FROM journal_attempts WHERE id=%[2]s;
 UPDATE journal_attempts SET version=version+1,data=json_set(data,'$.version',version+1,'$.state',%[5]s,'$.Message','Owner control applied','$.UpdatedAt',%[6]s)
 WHERE id=%[2]s AND EXISTS(SELECT 1 FROM journal_controls WHERE command_id=%[1]s AND status='apply_waiting');
 UPDATE journal_controls SET status=CASE op WHEN 'stop' THEN 'finish_pending' ELSE 'completed' END WHERE command_id=%[1]s AND status='apply_waiting';
 SELECT status FROM journal_controls WHERE command_id=%[1]s AND attempt_id=%[2]s AND op=%[3]s AND subject_version=%[4]d;
 COMMIT;`, key, attempt, operation, version, literal(target), literal(now())))
	if err != nil {
		return "", err
	}
	status := strings.TrimSpace(string(b))
	if status == "finish_pending" {
		a, e := Get(id)
		if e != nil {
			return "", e
		}
		// After a waiting attempt is stopped, release its issue reservation before
		// acknowledging completion. Retain the receipt if the service is unreachable.
		if e = hook(AfterFinish, &a); e != nil {
			return "received", nil
		}
		if _, e = query("UPDATE journal_controls SET status='completed' WHERE command_id=" + key + " AND status='finish_pending';"); e != nil {
			return "", e
		}
		return "completed", nil
	}
	if status == "" {
		return "unavailable", nil
	}
	return status, nil
}
func pending(id string) (string, error) {
	b, err := query("SELECT op FROM journal_controls WHERE attempt_id=" + literal(id) + " AND status='received' ORDER BY CASE op WHEN 'stop' THEN 0 ELSE 1 END LIMIT 1;")
	return strings.TrimSpace(string(b)), err
}
func boundary(ctx context.Context, id string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	op, err := pending(id)
	if err != nil {
		return err
	}
	if op != "" {
		return errors.New("owner control pending")
	}
	return nil
}
func watchControls(ctx context.Context, id string, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if boundary(ctx, id) != nil {
				cancel()
				return
			}
		}
	}
}

// Commit final state and consume pending controls under the same SQLite lock.
// A crash before this transaction leaves the control in received for reconciliation.
func settle(a *Attempt) (bool, error) {
	old := a.Version
	a.Version++
	a.UpdatedAt = now()
	key := literal(a.ID)
	b, err := query(fmt.Sprintf(`BEGIN IMMEDIATE;
 UPDATE journal_attempts SET version=%d,data=CASE WHEN EXISTS(SELECT 1 FROM journal_controls WHERE attempt_id=%s AND status='received')
 THEN json_set(%s,'$.state',CASE WHEN EXISTS(SELECT 1 FROM journal_controls WHERE attempt_id=%s AND status='received' AND op='stop') THEN 'cancelled' ELSE 'waiting' END,'$.Message','Owner control applied')
 ELSE %s END WHERE id=%s AND version=%d;
 SELECT changes(); SELECT CASE WHEN EXISTS(SELECT 1 FROM journal_controls WHERE attempt_id=%s AND status='received') THEN 1 ELSE 0 END;
 UPDATE journal_controls SET status='completed' WHERE attempt_id=%s AND status='received' AND EXISTS(SELECT 1 FROM journal_attempts WHERE id=%s AND version=%d);
 COMMIT;`, a.Version, key, packed(a), key, packed(a), key, old, key, key, key, a.Version))
	if err != nil {
		return false, err
	}
	lines := strings.Fields(string(b))
	if len(lines) != 2 || lines[0] != "1" {
		return false, errors.New("attempt changed during finalization")
	}
	if lines[1] == "1" {
		saved, e := Get(a.ID)
		if e != nil {
			return false, e
		}
		*a = saved
		return true, nil
	}
	return false, nil
}
