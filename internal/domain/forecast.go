// SPDX-License-Identifier: MPL-2.0
package domain

import "time"

type CompletionEvidence struct {
	Number     int        `json:"number"`
	Current    bool       `json:"current"`
	RecordedAt *time.Time `json:"recordedAt"`
}

type ProgressForecast struct {
	Status                 string `json:"status"`
	AsOf                   string `json:"asOf"`
	CompletedTickets       int    `json:"completedTickets"`
	TotalTickets           int    `json:"totalTickets"`
	CompletedEstimateHours int    `json:"completedEstimateHours"`
	TotalEstimateHours     int    `json:"totalEstimateHours"`
	Date                   string `json:"date,omitempty"`
	TargetDeltaDays        *int   `json:"targetDeltaDays,omitempty"`
	Reason                 string `json:"reason,omitempty"`
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ForecastProgress projects the remaining approved estimates using the fraction
// of estimated work whose integration the owner has actually attested.
func ForecastProgress(p Plan, approved *time.Time, current, paused bool, asOf time.Time, evidence []CompletionEvidence) ProgressForecast {
	today := utcDay(asOf)
	out := ProgressForecast{Status: "unknown", AsOf: today.Format(time.DateOnly), TotalTickets: len(p.Tickets)}
	observations := make(map[int]CompletionEvidence, len(evidence))
	for _, e := range evidence {
		observations[e.Number] = e
	}
	var last time.Time
	observedDays := map[string]bool{}
	var stale bool
	for _, t := range p.Tickets {
		out.TotalEstimateHours += t.EstimateHours
		e, ok := observations[t.Number]
		if !ok || !e.Current {
			stale = true
			continue
		}
		if approved == nil || e.RecordedAt == nil || e.RecordedAt.Before(*approved) || e.RecordedAt.After(asOf) {
			continue
		}
		out.CompletedTickets++
		out.CompletedEstimateHours += t.EstimateHours
		observedDays[e.RecordedAt.UTC().Format(time.DateOnly)] = true
		if last.Before(*e.RecordedAt) {
			last = *e.RecordedAt
		}
	}
	switch {
	case approved == nil:
		out.Reason = "Approval is needed before recording delivery progress."
	case !current:
		out.Reason = "A newer approved plan replaced this scope."
	case stale:
		out.Reason = "A canonical issue revision needs a current read or a fresh plan binding."
	case out.TotalTickets == 0:
		out.Reason = "The plan has no estimated tickets."
	case out.CompletedTickets == out.TotalTickets:
		out.Status = "complete"
		out.Date = last.UTC().Format(time.DateOnly)
	case paused:
		out.Reason = "Dispatch is paused. The resumption date is unknown."
	case len(observedDays) < 2 || out.CompletedEstimateHours <= 0:
		out.Reason = "Record integration evidence on at least two different UTC dates to estimate the remaining work."
	default:
		elapsed := int((today.Unix()-utcDay(*approved).Unix())/86400) + 1
		remain := out.TotalEstimateHours - out.CompletedEstimateHours
		// Ceiling division keeps a partial projected day visible.
		days := (remain*elapsed + out.CompletedEstimateHours - 1) / out.CompletedEstimateHours
		finish := today.AddDate(0, 0, days)
		if finish.Year() > 9999 {
			out.Reason = "The projection exceeds the supported calendar range."
			break
		}
		out.Status = "projected"
		out.Date = finish.Format(time.DateOnly)
	}
	target, err := time.Parse(time.DateOnly, p.Target)
	if err == nil && out.Date != "" {
		end, _ := time.Parse(time.DateOnly, out.Date)
		delta := int((end.Unix() - target.Unix()) / 86400)
		out.TargetDeltaDays = &delta
	}
	return out
}
