// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
	"github.com/lucasmkrx/bugsnitch/internal/turns"
)

const meterQueue = `CREATE TABLE IF NOT EXISTS turn_meter_outbox (
 id INTEGER PRIMARY KEY AUTOINCREMENT,device TEXT NOT NULL,project TEXT NOT NULL,subject TEXT NOT NULL,sequence INTEGER NOT NULL,
 general INTEGER NOT NULL,model TEXT NOT NULL,input INTEGER NOT NULL,output INTEGER NOT NULL,
 UNIQUE(device,subject,sequence,general)); `

var modelID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,119}$`)

func measurable(r agent.Result) bool {
	return r.Usage != nil && modelID.MatchString(r.Model) && incidents.Redact(r.Model) == r.Model && r.Usage.InputTokens >= 0 && r.Usage.InputTokens <= 1<<32 && r.Usage.OutputTokens >= 0 && r.Usage.OutputTokens <= 1<<32
}
func insertMeasurement(selectSubject string) {
	_, err := journal(meterQueue + "INSERT OR IGNORE INTO turn_meter_outbox(device,project,subject,sequence,general,model,input,output) SELECT " + selectSubject + ";")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not journal model telemetry; this admitted turn may remain unmetered.")
	}
}
func amounts(r agent.Result) string {
	return sqlValue(r.Model) + "," + strconv.FormatInt(r.Usage.InputTokens, 10) + "," + strconv.FormatInt(r.Usage.OutputTokens, 10)
}

func MeasureWorker(a runner.Summary, r agent.Result) {
	if !measurable(r) {
		return
	}
	// acked identifies the successful turn_start. Run before sending turn_end.
	insertMeasurement("a.device,p.value->>'$.id',a.attempt,a.acked,0," + amounts(r) +
		" FROM authority a,profile f,json_each(f.data,'$.Enrollment.projects') p WHERE a.attempt=" + sqlValue(a.ID) +
		" AND f.id=1 AND a.device=json_extract(f.data,'$.Enrollment.deviceId') AND a.pending='' AND lower(p.value->>'$.repository')=" + sqlValue(strings.ToLower(a.Repository)))
}
func MeasureGeneral(op turns.Operation, r agent.Result) {
	if !measurable(r) || op.Device == "" {
		return
	}
	insertMeasurement(sqlValue(op.Device) + "," + sqlValue(op.Project) + "," + sqlValue(op.ID) + ",1,1," + amounts(r))
}
func flushMetering(ctx context.Context, p profile, assigned protocol.Enrollment) error {
	projects := []string{"''"}
	for _, project := range assigned.Projects {
		projects = append(projects, sqlValue(project.ID))
	}
	filter := "device=" + sqlValue(assigned.DeviceID) + " AND project IN (" + strings.Join(projects, ",") + ")"
	high, err := journal(meterQueue + "SELECT coalesce(max(rowid),0) FROM turn_meter_outbox WHERE " + filter)
	if err != nil {
		return err
	}
	cutoff, err := strconv.ParseInt(high, 10, 64)
	if err != nil {
		return err
	}
	filter += " AND rowid<=" + strconv.FormatInt(cutoff, 10)
	for {
		raw, err := journal("SELECT json_group_array(json_object('row',rowid,'subject',subject,'sequence',sequence,'general',json(CASE general WHEN 1 THEN 'true' ELSE 'false' END),'model',model,'input',input,'output',output)) FROM (SELECT rowid,* FROM turn_meter_outbox WHERE " + filter + " ORDER BY rowid LIMIT 32)")
		if err != nil {
			return err
		}
		var rows []struct {
			Row int64
			protocol.TurnMeter
		}
		if err = json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) == 0 {
			return err
		}
		input := protocol.Sync{Protocol: 1}
		for _, row := range rows {
			input.Metering = append(input.Metering, row.TurnMeter)
		}
		var ack protocol.Enrollment
		if err = send(ctx, p, "sync", input, &ack); err != nil {
			return err
		}
		if !ack.TurnMetering || ack.DeviceID != assigned.DeviceID || ack.Workspace != assigned.Workspace {
			return fmt.Errorf("metering acknowledgement differs from enrollment")
		}
		if _, err = journal("DELETE FROM turn_meter_outbox WHERE " + filter + " AND rowid<=" + strconv.FormatInt(rows[len(rows)-1].Row, 10)); err != nil {
			return err
		}
	}
}
