// SPDX-License-Identifier: MPL-2.0
package domain

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Plan versions are immutable owner proposals. Estimates and target dates have separate meanings.
type Plan struct {
	Title       string   `json:"title"`
	Milestone   string   `json:"milestone"`
	Assumptions string   `json:"assumptions"`
	Target      string   `json:"target,omitempty"`
	Forecast    string   `json:"forecast,omitempty"`
	Tickets     []Ticket `json:"tickets"`
}
type Ticket struct {
	Number        int      `json:"number"`
	Title         string   `json:"title"`
	Criteria      []string `json:"criteria"`
	Verification  string   `json:"verification"`
	Priority      int      `json:"priority"`
	EstimateHours int      `json:"estimateHours"`
	Dependencies  []int    `json:"dependencies"`
	Resources     []string `json:"resources"`
}

var resourceName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func (p Plan) Order() ([]int, error) {
	if strings.TrimSpace(p.Title) == "" || len(p.Title) > 160 || strings.TrimSpace(p.Milestone) == "" || len(p.Milestone) > 240 || len(p.Assumptions) > 2000 || len(p.Tickets) == 0 || len(p.Tickets) > 64 {
		return nil, errors.New("provide a deliverable, milestone and 1–64 tickets")
	}
	for _, date := range []string{p.Target, p.Forecast} {
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				return nil, errors.New("dates must use YYYY-MM-DD")
			}
		}
	}
	tickets := map[int]Ticket{}
	for _, t := range p.Tickets {
		if t.Number <= 0 || tickets[t.Number].Number != 0 {
			return nil, errors.New("issue numbers must be unique and positive")
		}
		if strings.TrimSpace(t.Title) == "" || len(t.Title) > 200 || strings.TrimSpace(t.Verification) == "" || len(t.Verification) > 1000 || len(t.Criteria) == 0 || len(t.Criteria) > 16 {
			return nil, errors.New("each issue needs a title, acceptance criteria and verification method")
		}
		if t.EstimateHours < 1 || t.EstimateHours > 16 || t.Priority < 0 || t.Priority > 4 {
			return nil, errors.New("split tickets over 16 hours and choose priority 0–4")
		}
		for _, c := range t.Criteria {
			if strings.TrimSpace(c) == "" || len(c) > 1000 {
				return nil, errors.New("invalid acceptance criterion")
			}
		}
		if len(t.Resources) > 16 || len(t.Dependencies) > 64 {
			return nil, errors.New("too many dependencies or resource locks")
		}
		for _, resource := range t.Resources {
			if !resourceName.MatchString(resource) {
				return nil, errors.New("invalid shared resource name")
			}
		}
		tickets[t.Number] = t
	}
	done := map[int]bool{}
	order := make([]int, 0, len(tickets))
	for len(order) < len(tickets) {
		var ready []Ticket
		for n, t := range tickets {
			if done[n] {
				continue
			}
			eligible := true
			for _, dep := range t.Dependencies {
				if tickets[dep].Number == 0 {
					return nil, errors.New("dependency refers to an unlisted issue")
				}
				if !done[dep] {
					eligible = false
				}
			}
			if eligible {
				ready = append(ready, t)
			}
		}
		if len(ready) == 0 {
			return nil, errors.New("dependencies contain a cycle")
		}
		slices.SortFunc(ready, func(a, b Ticket) int {
			if a.Priority != b.Priority {
				return a.Priority - b.Priority
			}
			if a.Number < b.Number {
				return -1
			}
			if a.Number > b.Number {
				return 1
			}
			return 0
		})
		n := ready[0].Number
		done[n] = true
		order = append(order, n)
	}
	return order, nil
}

func CriteriaHash(criteria []string) string {
	b, _ := json.Marshal(criteria)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
