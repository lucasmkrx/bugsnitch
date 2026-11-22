// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func (s *Server) nextIssue(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var in protocol.NextRequest
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if err := compatible(in.Protocol); err != nil {
		return err
	}
	rec, err := s.recommend(r.Context(), token, in.Project)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return send(w, 200, rec)
}

// Rust computes eligibility and order from one database snapshot. At the HTTP
// boundary, apply the same sharing policy used for every other incident or
// ticket before allowing criteria from the stored plan to leave the server.
func (s *Server) recommend(ctx context.Context, token, p string) (protocol.Recommendation, error) {
	var snapshot struct {
		Reason     string            `json:"reason"`
		Plan       domain.Plan       `json:"plan"`
		Scope      protocol.RunScope `json:"scope"`
		Candidates []struct {
			Number    int                      `json:"number"`
			Reason    string                   `json:"reason"`
			Selection *protocol.IssueSelection `json:"selection"`
		} `json:"candidates"`
	}
	var out protocol.Recommendation
	if err := s.coordinate(ctx, "claims.next", claimInput(token, protocol.NextRequest{Protocol: protocol.Version, Project: p}), &snapshot); err != nil {
		return out, err
	}
	if snapshot.Reason != "" {
		out.Reason = snapshot.Reason
		return out, nil
	}
	if _, err := snapshot.Plan.Order(); err != nil {
		return out, errors.New("invalid stored plan")
	}
	criteria := make(map[int][]string, len(snapshot.Plan.Tickets))
	for _, ticket := range snapshot.Plan.Tickets {
		criteria[ticket.Number] = ticket.Criteria
	}
	for _, candidate := range snapshot.Candidates {
		reason := candidate.Reason
		if candidate.Selection != nil {
			list := criteria[candidate.Number]
			if candidate.Selection.Number != candidate.Number || len(list) == 0 {
				return out, errors.New("invalid issue selection")
			}
			shareable := true
			for _, criterion := range list {
				if incidents.Redact(criterion) != criterion {
					shareable = false
					break
				}
			}
			if shareable {
				candidate.Selection.Criteria = list
				return protocol.Recommendation{Scope: &snapshot.Scope, Selection: candidate.Selection}, nil
			}
			reason = "the approved criteria require redaction before sharing"
		}
		if out.Reason == "" {
			out.Reason = fmt.Sprintf("No eligible issue. Highest-priority issue #%d: %s", candidate.Number, reason)
		}
	}
	return out, nil
}
