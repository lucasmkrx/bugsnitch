// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
)

var MeasureGeneral func(turns.Operation, agent.Result)
var BindGeneral func(workspace, repository, role, id string) (turns.Operation, error)
var SubmitGeneral func(context.Context, turns.Operation) error

const modelSlots = "CREATE TABLE IF NOT EXISTS model_permits(id TEXT PRIMARY KEY,role TEXT NOT NULL,created_at TEXT NOT NULL); "
const generalTable = "CREATE TABLE IF NOT EXISTS general_sessions(id TEXT PRIMARY KEY,operation TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL); "

type GeneralSession struct {
	ID        string          `json:"id"`
	Operation turns.Operation `json:"operation"`
	Status    string          `json:"status"`
	CreatedAt string          `json:"createdAt"`
}

func GeneralSessions() ([]GeneralSession, error) {
	b, err := query(generalTable + "SELECT json_group_array(json_object('id',id,'operation',json(operation),'status',status,'createdAt',created_at)) FROM general_sessions WHERE status<>'ended' ORDER BY created_at;")
	if err != nil {
		return nil, err
	}
	var sessions []GeneralSession
	err = json.Unmarshal(b, &sessions)
	return sessions, err
}
func submitGeneral(ctx context.Context, op turns.Operation, version int) error {
	if op.Device == "" {
		return nil
	}
	if SubmitGeneral == nil {
		return errors.New("missing registered-device permit transport")
	}
	op.Version = version
	return SubmitGeneral(ctx, op)
}
func changeGeneral(id, from, to string) error {
	b, err := query("UPDATE general_sessions SET status=" + literal(to) + " WHERE id=" + literal(id) + " AND status=" + literal(from) + "; SELECT changes();")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) != "1" {
		return errors.New("local turn changed; reconcile on original machine")
	}
	return nil
}
func closeGeneral(op turns.Operation) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := submitGeneral(ctx, op, 2); err != nil {
		return fmt.Errorf("turn ended locally, run turn-reconcile --id %s: %w", op.ID, err)
	}
	_, err := query("BEGIN IMMEDIATE; DELETE FROM model_permits WHERE id=" + literal(op.ID) + " AND EXISTS(SELECT 1 FROM general_sessions WHERE id=" + literal(op.ID) + " AND status='ending'); UPDATE general_sessions SET status='ended' WHERE id=" + literal(op.ID) + " AND status='ending'; COMMIT;")
	return err
}
func ReconcileGeneral(id string, confirmed bool) error {
	unlock, err := attemptLock(id)
	if err != nil {
		return err
	}
	defer unlock()
	b, err := query(generalTable + "SELECT json_object('operation',json(operation),'status',status) FROM general_sessions WHERE id=" + literal(id) + ";")
	if err != nil {
		return err
	}
	var s GeneralSession
	if json.Unmarshal(b, &s) != nil {
		return errors.New("unknown general turn")
	}
	if s.Status == "ended" {
		return nil
	}
	if s.Status == "running" && !confirmed {
		return errors.New("runtime termination unverified; inspect the original machine before confirming quiescence")
	}
	if s.Status != "ending" {
		if err = changeGeneral(id, s.Status, "ending"); err != nil {
			return err
		}
	}
	return closeGeneral(s.Operation)
}
func generalModel(ctx context.Context, ws, repo, role, id, instructions, prompt, model, effort string, schema any) (agent.Result, error) {
	return generalModelUsing(ctx, ws, repo, role, id, instructions, prompt, model, effort, schema, readOnlyModel)
}

type readOnlyTurn func(context.Context, string, string, string, string, any) (agent.Result, error)

func generalModelUsing(ctx context.Context, ws, repo, role, id, instructions, prompt, model, effort string, schema any, invoke readOnlyTurn) (agent.Result, error) {
	var empty agent.Result
	unlock, err := attemptLock(id)
	if err != nil {
		return empty, err
	}
	defer unlock()
	if BindGeneral == nil {
		return empty, errors.New("registered-device identity resolver unavailable")
	}
	op, err := BindGeneral(ws, repo, role, id)
	if err != nil {
		return empty, err
	}
	op.Version = 1
	if op.ID != id || op.Role != role || op.Workspace != ws || (op.Device != "" && !op.Valid()) {
		return empty, errors.New("invalid model permit identity")
	}
	// Persist both local ownership and remote identity before any external RPC.
	q := "BEGIN IMMEDIATE; " + modelSlots + generalTable + "INSERT INTO model_permits SELECT " + literal(id) + "," + literal(role) + "," + literal(now()) + " WHERE " + modelSlotAvailable + "; INSERT INTO general_sessions SELECT " + literal(id) + "," + packed(op) + ",'acquiring'," + literal(now()) + " WHERE EXISTS(SELECT 1 FROM model_permits WHERE id=" + literal(id) + "); SELECT changes(); COMMIT;"
	b, err := query(q)
	if err != nil {
		return empty, err
	}
	if strings.TrimSpace(string(b)) != "1" {
		return empty, errModelCapacity
	}
	if err = submitGeneral(ctx, op, 1); err != nil {
		if e := changeGeneral(id, "acquiring", "ending"); e != nil {
			return empty, errors.Join(err, e)
		}
		return empty, errors.Join(err, closeGeneral(op))
	}
	if err = changeGeneral(id, "acquiring", "running"); err != nil {
		return empty, err
	}
	turnCtx, cancel := context.WithCancel(ctx)
	authority := make(chan error, 1)
	if op.Device != "" {
		go monitorGeneral(turnCtx, op, cancel, authority)
	} else {
		authority <- nil
	}
	result, runErr := invoke(turnCtx, instructions, prompt, model, effort, schema)
	if result.Usage != nil && op.Device != "" && MeasureGeneral != nil {
		MeasureGeneral(op, result)
	}
	cancel()
	if e := <-authority; e != nil {
		runErr = errors.Join(runErr, e)
	}
	// The model request has returned and any app-server process has exited.
	if err = changeGeneral(id, "running", "ending"); err != nil {
		return result, errors.Join(runErr, err)
	}
	return result, errors.Join(runErr, closeGeneral(op))
}

var readOnlyModel = func(ctx context.Context, instructions, prompt, model, effort string, schema any) (agent.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	return openAIOrCodex(ctx, instructions, prompt, model, effort, schema)
}

func Proof(ctx context.Context) (agent.Result, error) {
	id, err := newID()
	if err != nil {
		return agent.Result{}, err
	}
	return generalModel(ctx, "personal", "", "proof", id, "Respond only with the requested marker. Use no tools.", "Reply with exactly BUGSNITCH_LOCAL_OK.", "", "", nil)
}

// Revalidate the original, idempotent acquisition without obtaining another
// slot. Stop promptly when authority changes or can no longer be checked.
func monitorGeneral(ctx context.Context, op turns.Operation, cancel context.CancelFunc, done chan<- error) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			check, stop := context.WithTimeout(ctx, 8*time.Second)
			e := submitGeneral(check, op, 1)
			stop()
			if e != nil {
				if ctx.Err() != nil {
					done <- nil
					return
				}
				cancel()
				done <- fmt.Errorf("local model authority unavailable: %w", e)
				return
			}
		}
	}
}
