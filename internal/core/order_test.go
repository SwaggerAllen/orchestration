package core

import (
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// layerOf finds which layer a key landed in, by name.
func layerOf(o *Order, key string) string {
	for _, l := range o.Layers {
		for _, t := range l.Tickets {
			if t.Key == key {
				return l.Name
			}
		}
	}
	return ""
}

func ticketIn(o *Order, key string) *OrderedTicket {
	for _, l := range o.Layers {
		for i, t := range l.Tickets {
			if t.Key == key {
				return &l.Tickets[i]
			}
		}
	}
	return nil
}

// The five layers, each one earned by a different fact about waiting.
func TestOrderLayers(t *testing.T) {
	// A: in flight. B: nothing blocks it. C: blocked only by A, so it
	// frees itself as A lands. D: blocked by B, which has not started —
	// so it needs the "ready now" layer to be taken first. E: blocked by
	// D, which is itself blocked, so its depth is not yet decidable.
	a := tk("A", protocol.InProgress)
	b := tk("B", protocol.Todo)
	c := tk("C", protocol.Todo, func(t *Ticket) { t.BlockedBy = []string{"A"} })
	d := tk("D", protocol.Todo, func(t *Ticket) { t.BlockedBy = []string{"B"} })
	e := tk("E", protocol.Backlog, func(t *Ticket) { t.BlockedBy = []string{"D"} })
	a.Blocks = []string{"C"}
	b.Blocks = []string{"D"}
	d.Blocks = []string{"E"}

	o := ComputeOrder(snap(a, b, c, d, e), "")
	for _, want := range []struct{ key, layer string }{
		{"A", LayerStarted},
		{"B", LayerReady},
		{"C", LayerAfterFlight},
		{"D", LayerAfterReady},
		{"E", LayerRest},
	} {
		if got := layerOf(o, want.key); got != want.layer {
			t.Errorf("%s is in %q, want %q", want.key, got, want.layer)
		}
	}
}

// A resolved blocker is history. Listing it would make a ticket that can
// start today read as held up, which is the one way this report could
// cost more than it saves.
func TestOrderIgnoresResolvedBlockers(t *testing.T) {
	done := tk("A", protocol.Done)
	b := tk("B", protocol.Todo, func(t *Ticket) { t.BlockedBy = []string{"A"} })

	o := ComputeOrder(snap(done, b), "")
	if got := layerOf(o, "B"); got != LayerReady {
		t.Errorf("B is in %q, want %q — a Done blocker still counted", got, LayerReady)
	}
	if got := layerOf(o, "A"); got != "" {
		t.Errorf("a resolved ticket appears in %q; it is not waiting and nothing waits on it", got)
	}
	if ot := ticketIn(o, "B"); ot != nil && len(ot.BlockedBy) != 0 {
		t.Errorf("B lists %d blocker(s) after they resolved", len(ot.BlockedBy))
	}
}

// Each ticket carries what a reader needs to act without a second
// lookup: what it is, where it is, what holds it, and what it holds up.
func TestOrderCarriesBothDirections(t *testing.T) {
	a := tk("A", protocol.InProgress, func(t *Ticket) {
		t.Title = "The blocker"
		t.URL = "https://tracker.invalid/issue/A"
		t.Blocks = []string{"B"}
	})
	b := tk("B", protocol.Todo, func(t *Ticket) {
		t.Title = "The blocked one"
		t.BlockedBy = []string{"A"}
	})

	o := ComputeOrder(snap(a, b), "")
	ot := ticketIn(o, "B")
	if ot == nil {
		t.Fatal("B is missing")
	}
	if len(ot.BlockedBy) != 1 || ot.BlockedBy[0].Key != "A" || ot.BlockedBy[0].Title != "The blocker" {
		t.Errorf("B's blockers = %+v, want A named and titled", ot.BlockedBy)
	}
	if ot.BlockedBy[0].State != protocol.InProgress {
		t.Errorf("the blocker's state is missing; whether it is moving is the point")
	}
	up := ticketIn(o, "A")
	if up == nil || len(up.Blocks) != 1 || up.Blocks[0].Key != "B" {
		t.Errorf("A does not report what it blocks: %+v", up)
	}
	if up.URL == "" {
		t.Error("no link; the report is meant to be clicked rather than transcribed")
	}
}

// A mutex collision is not a blocker and must not be reported as one:
// design may run on both at once, and only promotion into Ready for dev
// is reverted (DESIGN §6). But a ticket that reads as ready while it is
// really queued is exactly the design most likely to be re-evaluated
// before it lands (§7), so it is named.
func TestOrderNamesMutexCollisionsWithoutTreatingThemAsBlockers(t *testing.T) {
	held := tk("A", protocol.InProgress, func(t *Ticket) { t.Labels = []string{"system:engine"} })
	waiting := tk("B", protocol.Todo, func(t *Ticket) { t.Labels = []string{"system:engine"} })

	o := ComputeOrder(snap(held, waiting), "")
	if got := layerOf(o, "B"); got != LayerReady {
		t.Errorf("B is in %q, want %q — a mutex label is not a blocker", got, LayerReady)
	}
	ot := ticketIn(o, "B")
	if ot == nil || len(ot.MutexHeldBy) != 1 {
		t.Fatalf("B does not report the collision: %+v", ot)
	}
	if ot.MutexHeldBy[0].Key != "A" || ot.MutexHeldBy[0].Label != "system:engine" {
		t.Errorf("the collision does not name the holder and the label: %+v", ot.MutexHeldBy[0])
	}
	if len(ot.BlockedBy) != 0 {
		t.Error("the collision was reported as a blocker")
	}
	// The in-flight ticket holds the label rather than colliding with it.
	if a := ticketIn(o, "A"); a != nil && len(a.MutexHeldBy) != 0 {
		t.Error("the ticket holding the label reports colliding with itself")
	}
}

// Blocked is started: it has left Todo, it is not a candidate to start,
// and the tickets waiting on it are waiting on something that will not
// move by itself — which is only visible if it sits with the in-flight
// work and prints its state.
func TestOrderPutsBlockedWithTheInFlightWork(t *testing.T) {
	stuck := tk("A", protocol.Blocked, func(t *Ticket) { t.Blocks = []string{"B"} })
	waiting := tk("B", protocol.Todo, func(t *Ticket) { t.BlockedBy = []string{"A"} })

	o := ComputeOrder(snap(stuck, waiting), "")
	if got := layerOf(o, "A"); got != LayerStarted {
		t.Errorf("a Blocked ticket is in %q, want %q", got, LayerStarted)
	}
	ot := ticketIn(o, "B")
	if ot == nil || len(ot.BlockedBy) != 1 || ot.BlockedBy[0].State != protocol.Blocked {
		t.Fatalf("B does not show that its blocker is stuck: %+v", ot)
	}
}

// The boundary ticket is machinery with its own protocol (DESIGN §10),
// not work to schedule.
func TestOrderExcludesTheBoundaryTicket(t *testing.T) {
	b := tk("A", protocol.Todo, func(t *Ticket) { t.Labels = []string{LabelBoundary} })
	if got := layerOf(ComputeOrder(snap(b), ""), "A"); got != "" {
		t.Errorf("the boundary ticket appears in %q", got)
	}
}

// Scoping to a milestone is what makes this readable on a real backlog.
func TestOrderFiltersByMilestone(t *testing.T) {
	mine := tk("A", protocol.Todo, func(t *Ticket) { t.Milestone = "M1" })
	other := tk("B", protocol.Todo, func(t *Ticket) { t.Milestone = "M2" })

	o := ComputeOrder(snap(mine, other), "M1")
	if layerOf(o, "A") == "" {
		t.Error("the milestone's own ticket is missing")
	}
	if got := layerOf(o, "B"); got != "" {
		t.Errorf("a ticket from another milestone appears in %q", got)
	}
}

// Every layer explains itself. The report is read by someone deciding
// what to do next, and a bare heading makes them guess at the rule.
func TestEveryLayerSaysWhatItMeans(t *testing.T) {
	o := ComputeOrder(snap(tk("A", protocol.Todo)), "")
	if len(o.Layers) != 5 {
		t.Fatalf("got %d layers, want 5", len(o.Layers))
	}
	for _, l := range o.Layers {
		if strings.TrimSpace(l.Why) == "" {
			t.Errorf("layer %q has no explanation", l.Name)
		}
	}
}
