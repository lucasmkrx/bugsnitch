// SPDX-License-Identifier: MPL-2.0
package domain

import (
	"testing"
	"time"
)

func moment(t string) *time.Time {
	v, err := time.Parse(time.RFC3339, t)
	if err != nil {
		panic(err)
	}
	return &v
}
func TestProgressForecastTracksWeightedDeliveredScopeAndElapsedTime(t *testing.T) {
	p := Plan{Target: "2026-11-13", Forecast: "2026-11-12", Tickets: []Ticket{{Number: 3, EstimateHours: 1}, {Number: 1, EstimateHours: 5}, {Number: 2, EstimateHours: 6}}}
	approved := moment("2026-11-01T18:00:00Z")
	row := []CompletionEvidence{{1, true, moment("2026-11-03T18:00:00Z")}, {2, true, nil}, {3, true, moment("2026-11-05T23:00:00Z")}}
	asOf := moment("2026-11-06T06:00:00Z")
	got := ForecastProgress(p, approved, true, false, *asOf, row)
	// Six of twelve estimate-hours attested across six UTC dates. Six remain.
	if got.Date != "2026-11-12" || got.Status != "projected" || got.CompletedEstimateHours != 6 || got.TotalEstimateHours != 12 || got.CompletedTickets != 2 || got.TargetDeltaDays == nil || *got.TargetDeltaDays != -1 {
		t.Fatal(got)
	}
	// No new completions for four calendar days lowers the observed pace.
	got = ForecastProgress(p, approved, true, false, *moment("2026-11-10T06:00:00Z"), row)
	if got.Date != "2026-11-20" || *got.TargetDeltaDays != 7 || p.Forecast != "2026-11-12" || p.Target != "2026-11-13" {
		t.Fatal(got, p)
	}
	row[1].RecordedAt = moment("2026-11-07T10:15:00Z")
	got = ForecastProgress(p, approved, true, true, *moment("2026-11-10T06:00:00Z"), row)
	if got.Status != "complete" || got.Date != "2026-11-07" || got.CompletedTickets != 3 || *got.TargetDeltaDays != -6 {
		t.Fatal(got)
	}
}

func TestProgressForecastWithholdsDatesWithoutEvidence(t *testing.T) {
	approved := moment("2026-11-01T00:00:00Z")
	now := *moment("2026-11-05T01:00:00Z")
	p := Plan{Tickets: []Ticket{{Number: 1, EstimateHours: 2}, {Number: 2, EstimateHours: 1}, {Number: 3, EstimateHours: 3}}}
	evidence := []CompletionEvidence{{1, true, moment("2026-11-02T16:00:00Z")}, {2, true, moment("2026-11-02T19:00:00Z")}, {3, true, nil}}
	check := func(a *time.Time, current, paused bool) {
		t.Helper()
		f := ForecastProgress(p, a, current, paused, now, evidence)
		if f.Status != "unknown" || f.Reason == "" || f.Date != "" || f.TargetDeltaDays != nil {
			t.Fatal(f)
		}
	}
	check(nil, true, false)
	check(approved, true, false)
	evidence[1].RecordedAt = moment("2026-11-09T00:00:00Z")
	check(approved, true, false)
	evidence[1].RecordedAt = moment("2026-11-04T23:00:00Z")
	check(approved, false, false)
	check(approved, true, true)
	evidence[0].Current = false
	check(approved, true, false)
	if f := ForecastProgress(p, approved, true, false, now, evidence); f.CompletedTickets != 1 {
		t.Fatal("stale subject counted", f)
	}
}
