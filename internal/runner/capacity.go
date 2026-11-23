// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const MaxLocalCapacity = 32

var ErrTicketCapacity = errors.New("local implementation slots are reserved")
var ErrHostedTurnCapacity = errors.New("workspace or device model-turn slots are reserved")

type LocalLimits struct {
	Tickets int `json:"tickets"`
	Turns   int `json:"turns"`
}

// SetLocalLimits updates the current runner profile. A zero leaves that limit
// unchanged, so concurrent updates to different limits preserve each other.
func SetLocalLimits(tickets, turns int) (LocalLimits, error) {
	var limits LocalLimits
	if tickets < 0 || tickets > MaxLocalCapacity || turns < 0 || turns > MaxLocalCapacity {
		return limits, errors.New("local limits must be between 1 and 32")
	}
	const read = "SELECT json_object('tickets',ticket_limit,'turns',turn_limit) FROM runner_capacity WHERE id=1;"
	sql := read
	if tickets != 0 || turns != 0 {
		sql = fmt.Sprintf("BEGIN IMMEDIATE; UPDATE runner_capacity SET ticket_limit=coalesce(nullif(%d,0),ticket_limit),turn_limit=coalesce(nullif(%d,0),turn_limit) WHERE id=1; %s COMMIT;", tickets, turns, read)
	}
	b, err := query(sql)
	if err != nil {
		return limits, err
	}
	err = json.Unmarshal(b, &limits)
	return limits, err
}

// A batch may own more tickets than available turns. Wait only for a known
// capacity denial. Other failures (including uncertain acknowledgments) must
// follow the ordinary recovery path without starting another turn.
func reserveWorkerTurn(ctx context.Context, a *Attempt) (func() error, error) {
	wait, cancel := context.WithTimeoutCause(ctx, time.Duration(a.Project.Config.forStage(a.Phase).TimeoutSeconds)*time.Second, errModelCapacity)
	defer cancel()
	for {
		if err := context.Cause(wait); err != nil {
			return nil, err
		}
		release, err := acquireModel(a.ID)
		if err == nil {
			err = context.Cause(wait)
			if err == nil && BeforeTurn != nil {
				err = BeforeTurn(wait, admissionSubject(a))
			}
			if err == nil {
				if expired := context.Cause(wait); expired != nil {
					// An acknowledgment can cross the deadline. Release this
					// known grant without ever invoking the model.
					return nil, errors.Join(expired, hook(AfterTurn, a), release())
				}
				return release, nil
			}
			if releaseErr := release(); releaseErr != nil {
				return nil, errors.Join(err, releaseErr)
			}
		}
		if a.LocalRunScope == nil || (!errors.Is(err, errModelCapacity) && !errors.Is(err, ErrHostedTurnCapacity)) {
			return nil, err
		}
		if _, err := lease(wait, subject(a), false); err != nil {
			return nil, err
		}
		select {
		case <-wait.Done():
			return nil, context.Cause(wait)
		case <-time.After(time.Second):
		}
		if err := boundary(wait, a.ID); err != nil {
			return nil, err
		}
	}
}
