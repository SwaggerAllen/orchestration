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

// A shared scope is not a blocker and must not be reported as one —
// nothing refuses on it at all now (DESIGN §6). It is still named,
// because two tickets in one system are two branches that will meet in a
// merge, and §7's collision rule fires on exactly this overlap.
func TestOrderNamesASharedScopeWithoutTreatingItAsABlocker(t *testing.T) {
	held := tk("A", protocol.InProgress, func(t *Ticket) { t.Labels = []string{"system:engine"} })
	waiting := tk("B", protocol.Todo, func(t *Ticket) { t.Labels = []string{"system:engine"} })

	o := ComputeOrder(snap(held, waiting), "")
	if got := layerOf(o, "B"); got != LayerReady {
		t.Errorf("B is in %q, want %q — a shared scope is not a blocker", got, LayerReady)
	}
	ot := ticketIn(o, "B")
	if ot == nil || len(ot.ScopeSharedWith) != 1 {
		t.Fatalf("B does not report the collision: %+v", ot)
	}
	if ot.ScopeSharedWith[0].Key != "A" || ot.ScopeSharedWith[0].Label != "system:engine" {
		t.Errorf("the collision does not name the holder and the label: %+v", ot.ScopeSharedWith[0])
	}
	if len(ot.BlockedBy) != 0 {
		t.Error("the collision was reported as a blocker")
	}
	// The in-flight ticket holds the label rather than colliding with it.
	if a := ticketIn(o, "A"); a != nil && len(a.ScopeSharedWith) != 0 {
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

// Milestones are worked in sequence, and that constraint lives nowhere
// in the blocker graph: a later milestone's tickets are commonly filed
// with no dependencies at all, because the milestone is the dependency.
// Read literally they have nothing blocking them, so a project-wide
// ordering put them in "Ready now" beside work that can genuinely start
// today — the report recommending something that must not be started.
func TestOrderHoldsBackLaterMilestonesAcrossAWideScope(t *testing.T) {
	now := tk("A", protocol.Todo, func(t *Ticket) { t.Milestone = "M1" })
	later := tk("B", protocol.Todo, func(t *Ticket) { t.Milestone = "M2" }) // no blockers at all
	s := snap(now, later)
	s.CurrentMilestone = "M1"

	o := ComputeOrder(s, "")
	if got := layerOf(o, "A"); got != LayerReady {
		t.Errorf("the current milestone's ticket is in %q, want %q", got, LayerReady)
	}
	if got := layerOf(o, "B"); got == LayerReady {
		t.Error("a later milestone's unblocked ticket reads as startable now")
	}
	ot := ticketIn(o, "B")
	if ot == nil {
		t.Fatal("B vanished; held back is not the same as hidden")
	}
	if !strings.Contains(ot.Note, "M1") || !strings.Contains(ot.Note, "sequence") {
		t.Errorf("B does not say why it is held back: %q", ot.Note)
	}
	// The milestone is printed in this mode, since it is the reason.
	if ot.Milestone != "M2" {
		t.Errorf("milestone = %q, want M2", ot.Milestone)
	}
}

// Asking for one milestone by name is asking about that one. Demoting
// all of it would answer a question nobody put.
func TestScopingToAMilestoneDoesNotHoldItBack(t *testing.T) {
	later := tk("B", protocol.Todo, func(t *Ticket) { t.Milestone = "M2" })
	s := snap(later)
	s.CurrentMilestone = "M1"

	o := ComputeOrder(s, "M2")
	if got := layerOf(o, "B"); got != LayerReady {
		t.Errorf("B is in %q, want %q — the scope was the question", got, LayerReady)
	}
	if ot := ticketIn(o, "B"); ot != nil && ot.Note != "" {
		t.Errorf("a scoped report explains a constraint it is not applying: %q", ot.Note)
	}
}

// Work already started outside the current milestone is not held back —
// it is started, and pretending otherwise would hide it from the layer
// that says what the pipeline is doing.
func TestStartedWorkIsNeverHeldBackByItsMilestone(t *testing.T) {
	stray := tk("A", protocol.InProgress, func(t *Ticket) { t.Milestone = "M2" })
	s := snap(stray)
	s.CurrentMilestone = "M1"

	if got := layerOf(ComputeOrder(s, ""), "A"); got != LayerStarted {
		t.Errorf("in-flight work from another milestone is in %q, want %q", got, LayerStarted)
	}
}

// A ticket with no milestone is startable but uncommitted, and those
// are separate axes.
//
// It arrives that way honestly: the boundary files proposals into
// Triage, accepting one means moving it out and assigning a milestone,
// and the assignment is a commitment only the author can make (DESIGN
// §10) — so the accept happens and the assignment lags. Nothing
// sequences the ticket and neither dispatcher filters on milestone, so
// the queue will take it as soon as it reaches Designing.
//
// The wide view therefore names it as startable; a milestone's own
// scope does not, because that scope is the work committed to that
// milestone and nobody committed this. Measured on Catapult: ORC-48,
// ORC-50, ORC-51 and ORC-52 in Todo, and an order that named one ticket.
func TestOrderTreatsMilestonelessTicketsAsStartableButUncommitted(t *testing.T) {
	inM1 := tk("A", protocol.Todo, func(t *Ticket) { t.Milestone = "M1" })
	// tk() defaults to M1, so a genuinely bare ticket has to say so.
	bare := tk("B", protocol.Todo, func(t *Ticket) { t.Milestone = "" })
	later := tk("C", protocol.Todo, func(t *Ticket) { t.Milestone = "M2" })
	s := snap(inM1, bare, later)
	s.CurrentMilestone = "M1"

	// Wide scope: startable beside the current milestone's work, and not
	// gated with the later milestone — nothing sequences it.
	wide := ComputeOrder(s, "")
	if got := layerOf(wide, "B"); got != LayerReady {
		t.Errorf("bare ticket is in %q, want %q — nothing is in front of it", got, LayerReady)
	}
	if got := layerOf(wide, "C"); got == LayerReady {
		t.Error("a later milestone's ticket still must not read as startable")
	}

	// Narrow scope: absent from every milestone, including the current
	// one. A milestone's roster is what was committed to it.
	for _, m := range []string{"M1", "M2"} {
		if got := layerOf(ComputeOrder(s, m), "B"); got != "" {
			t.Errorf("bare ticket appeared under %s in %q — nobody committed it there", m, got)
		}
	}
	// And the milestone's own ticket is still there when asked for.
	if layerOf(ComputeOrder(s, "M1"), "A") == "" {
		t.Error("scoping to M1 lost M1's own ticket")
	}
}

// With no current milestone at all there is nothing to read a bare
// ticket as, so it stays bare and nothing is gated. The report is still
// useful; it just cannot make that claim.
func TestOrderWithNoCurrentMilestoneGatesNothing(t *testing.T) {
	bare := tk("A", protocol.Todo, func(t *Ticket) { t.Milestone = "" })
	other := tk("B", protocol.Todo, func(t *Ticket) { t.Milestone = "M2" })
	s := snap(bare, other)
	s.CurrentMilestone = ""

	o := ComputeOrder(s, "")
	for _, k := range []string{"A", "B"} {
		if got := layerOf(o, k); got != LayerReady {
			t.Errorf("%s is in %q, want %q", k, got, LayerReady)
		}
	}
}

// The narrowing on the rule above: a bare ticket earns its place in the
// wide view by being in Todo. Todo is somebody saying the work is ready
// and only the scheduling has lagged, which is the case worth surfacing.
// Backlog is an idea, and a triage-category state is a proposal nobody
// has accepted — listing those beside genuinely startable work turns a
// report meant to answer "what next" into the whole tracker.
func TestOrderTakesBareTicketsFromTodoOnward(t *testing.T) {
	ready := tk("A", protocol.Todo, func(t *Ticket) { t.Milestone = "" })
	idea := tk("B", protocol.Backlog, func(t *Ticket) { t.Milestone = "" })
	// Already moving, and still uncommitted. Dropping it would take work
	// that is happening right now out of the In-flight layer, which is
	// the one layer nobody can reconstruct from the tracker at a glance.
	moving := tk("D", protocol.InProgress, func(t *Ticket) { t.Milestone = "" })
	// The same states inside a milestone are unaffected: this rule is
	// about tickets nobody has committed, not about Backlog.
	committed := tk("C", protocol.Backlog, func(t *Ticket) { t.Milestone = "M1" })
	s := snap(ready, idea, moving, committed)
	s.CurrentMilestone = "M1"

	o := ComputeOrder(s, "")
	if got := layerOf(o, "A"); got != LayerReady {
		t.Errorf("a bare Todo ticket is in %q, want %q", got, LayerReady)
	}
	if got := layerOf(o, "D"); got != LayerStarted {
		t.Errorf("a bare in-flight ticket is in %q, want %q", got, LayerStarted)
	}
	if got := layerOf(o, "B"); got != "" {
		t.Errorf("a bare Backlog ticket appeared in %q — it is an idea, not work to start", got)
	}
	if layerOf(o, "C") == "" {
		t.Error("a Backlog ticket committed to the current milestone went missing")
	}
}

// Todo is also where a ticket lands when the author accepts a proposal
// and the milestone assignment lags, so "startable and uncommitted" and
// "committed and I forgot to say so" are indistinguishable here. The
// report does not try to tell them apart; it says the milestone is
// missing and lets the author recognise their own oversight.
func TestOrderFlagsUncommittedTickets(t *testing.T) {
	bare := tk("A", protocol.Todo, func(t *Ticket) { t.Milestone = "" })
	inM1 := tk("B", protocol.Todo, func(t *Ticket) { t.Milestone = "M1" })
	s := snap(bare, inM1)
	s.CurrentMilestone = "M1"

	got := map[string]OrderedTicket{}
	for _, l := range ComputeOrder(s, "").Layers {
		for _, ot := range l.Tickets {
			got[ot.Key] = ot
		}
	}
	if !got["A"].Uncommitted {
		t.Error("a ticket with no milestone is not flagged, so an unassigned one reads as scheduled")
	}
	if !strings.Contains(got["A"].Note, "oversight") {
		t.Errorf("the note does not prompt the author to check: %q", got["A"].Note)
	}
	if got["B"].Uncommitted {
		t.Error("a ticket in a milestone is flagged as uncommitted")
	}

	// Narrow scope drops bare tickets entirely, so nothing there is
	// uncommitted — and the flag must not fire on the whole roster just
	// because Milestone is left blank in that view.
	for _, l := range ComputeOrder(s, "M1").Layers {
		for _, ot := range l.Tickets {
			if ot.Uncommitted {
				t.Errorf("%s flagged uncommitted inside its own milestone's scope", ot.Key)
			}
		}
	}
}

// Ready for redesign is a queue, and a queue is work waiting rather than
// work happening — reading a bounced ticket as in flight is the same
// confusion splitting Designing from its queue removed.
func TestARedesignWaitingIsNotStartedWork(t *testing.T) {
	stray := tk("A", protocol.ReadyForRedesign, func(t *Ticket) { t.Milestone = "M2" })
	s := snap(stray)
	s.CurrentMilestone = "M1"
	if got := layerOf(ComputeOrder(s, ""), "A"); got == LayerStarted {
		t.Errorf("a ticket waiting in the redesign queue is in %q — it is queued, not in flight", got)
	}
}
