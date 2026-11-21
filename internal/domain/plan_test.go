package domain

import (
	"slices"
	"testing"
)

func TestPlanOrderAndCycle(t *testing.T) {
	t1 := Ticket{Number: 1, Title: "Base", Criteria: []string{"Builds"}, Verification: "Compile", Priority: 4, EstimateHours: 1}
	t2 := t1
	t2.Number = 2
	t2.Priority = 0
	t2.Dependencies = []int{1}
	t3 := t1
	t3.Number = 3
	t3.Priority = 1
	p := Plan{Title: "Delivery", Milestone: "Release", Tickets: []Ticket{t2, t1, t3}}
	order, err := p.Order()
	if err != nil || !slices.Equal(order, []int{3, 1, 2}) {
		t.Fatal(order, err)
	}
	p.Tickets[1].Dependencies = []int{2}
	if _, err = p.Order(); err == nil {
		t.Fatal("dependency cycle accepted")
	}
	p.Tickets[1].Dependencies = []int{7}
	if _, err = p.Order(); err == nil {
		t.Fatal("unknown prerequisite accepted")
	}
}
