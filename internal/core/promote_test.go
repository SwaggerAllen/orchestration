package core

import (
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// promotions returns every transition into the design queue this sweep
// plans. As a slice rather than a lookup because the count is half the
// rule: the queue is kept one deep.
func promotions(s *Snapshot) []string {
	var out []string
	for _, a := range Sweep(s) {
		if a.Kind == ActTransition && a.To == protocol.ReadyForDesign {
			out = append(out, a.TicketID)
		}
	}
	return out
}

func TestPromotesTheHeadOfTheOrder(t *testing.T) {
	// Same state and same age, so the precedence rule falls to key —
	// which is the tie-break the report sorts each layer by.
	got := promotions(snap(tk("T2", protocol.Todo), tk("T1", protocol.Todo)))
	if len(got) != 1 || got[0] != "T1" {
		t.Errorf("promoted %v, want T1 alone", got)
	}
}

// One at a time. A deeper queue buys no throughput — design is singular —
// and costs the ordering, because two tickets in one queue are separated
// by age rather than by the layering (DESIGN §8).
func TestQueueIsKeptOneDeep(t *testing.T) {
	s := snap(tk("Q", protocol.ReadyForDesign), tk("T1", protocol.Todo))
	if got := promotions(s); len(got) != 0 {
		t.Errorf("promoted %v while the queue was occupied", got)
	}
	if p := NextPromotion(s); !strings.Contains(p.Why, "Q") {
		t.Errorf("the report has to name what is occupying the queue, got %q", p.Why)
	}
}

// A ticket in Designing has left the queue, so the next one may take its
// place — the depth that matters is the queue's, not the agent's.
func TestDesigningDoesNotHoldTheQueue(t *testing.T) {
	// Claimed, not hand-dropped: a ticket sitting in an agent's state
	// with no record of how it got there is corrected back to the queue
	// that feeds the agent, which would occupy it for real.
	designing := tk("D", protocol.Designing, arrived(protocol.ReadyForDesign, RoleDesign))
	got := promotions(snap(designing, tk("T1", protocol.Todo)))
	if len(got) != 1 || got[0] != "T1" {
		t.Errorf("promoted %v, want T1 — Designing is not the queue", got)
	}
}

// The relaxation this rule exists for: design reads main, and a merged
// blocker's work is on main. Dev pickup keeps Resolved, which the last
// case here is the reason for.
func TestBlockerMustHaveReachedMerged(t *testing.T) {
	for _, c := range []struct {
		blocker protocol.State
		want    bool
	}{
		{protocol.Merged, true},
		{protocol.Done, true},
		{protocol.Canceled, true},
		{protocol.Reconciling, false},
		{protocol.Checks, false},
		{protocol.InProgress, false},
		{protocol.ReadyForDesign, false},
		// Merged once, then failed its post-deploy check. The work is on
		// main and a human owes a judgment on it, one of which is a
		// revert — so it is not something to design on top of.
		{protocol.Blocked, false},
	} {
		s := snap(tk("B", c.blocker), tk("T1", protocol.Todo, func(t *Ticket) { t.BlockedBy = []string{"B"} }))
		got := len(promotions(s)) == 1
		if got != c.want {
			t.Errorf("blocker in %s: promoted=%v, want %v", c.blocker, got, c.want)
		}
	}
}

// Assigning a milestone is the commitment, and it is the author's. A
// proposal accepted out of Triage with the assignment lagging is
// indistinguishable from a ticket left uncommitted on purpose, so the
// automation moves neither (DESIGN §10).
func TestMilestoneDecidesPromotion(t *testing.T) {
	for _, c := range []struct {
		name      string
		milestone string
		want      bool
	}{
		{"the current one", "M1", true},
		{"a later one", "M2", false},
		{"none at all", "", false},
	} {
		s := snap(tk("T1", protocol.Todo, func(t *Ticket) { t.Milestone = c.milestone }))
		if got := len(promotions(s)) == 1; got != c.want {
			t.Errorf("%s: promoted=%v, want %v", c.name, got, c.want)
		}
	}
}

// With no current milestone nothing is committed, so nothing promotes.
// Worth its own case because the obvious one-clause spelling of the
// milestone test — t.Milestone != s.CurrentMilestone — is false for every
// bare ticket here and would promote the whole backlog.
func TestNoCurrentMilestonePromotesNothing(t *testing.T) {
	s := snap(tk("T1", protocol.Todo, func(t *Ticket) { t.Milestone = "" }))
	s.CurrentMilestone = ""
	if got := promotions(s); len(got) != 0 {
		t.Errorf("promoted %v with no current milestone", got)
	}
}

// The pause is per ticket, and the exception is the tickets marked as
// blocking the boundary — the milestone's remaining scope, which the
// author committed to by filing them there (DESIGN §8, §10).
//
// Urgent is in this case to pin the half that did not move: it overrides
// the pause at dev pickup, where it means finishing work already designed,
// and a ticket in Todo is undesigned whatever its priority.
func TestOnlyBoundaryBlockersPromoteWhilePaused(t *testing.T) {
	b := tk("B1", protocol.Todo, func(t *Ticket) { t.Labels = []string{LabelBoundary} })
	blocker := tk("T1", protocol.Todo, func(t *Ticket) { t.Blocks = []string{"B1"} })
	urgent := tk("T2", protocol.Todo, func(t *Ticket) { t.Priority = 1 })
	got := promotions(snap(b, urgent, blocker))
	if len(got) != 1 || got[0] != "T1" {
		t.Errorf("promoted %v during the boundary pause, want T1 alone", got)
	}
}

// Promotion is upstream of the drain, so pausing it strands the very
// tickets the drain exists to run: a blocker filed during the pass opens
// in Todo and needs design to reach Ready for dev. Catapult's ORC-156 sat
// in Boundary review behind thirteen of them.
func TestABlockerFiledDuringThePassReachesTheDevQueue(t *testing.T) {
	b := tk("B1", protocol.Todo, func(t *Ticket) { t.Labels = []string{LabelBoundary} })
	blocker := tk("T1", protocol.Todo, func(t *Ticket) { t.Blocks = []string{"B1"} })
	s := snap(b, blocker)
	if got := promotions(s); len(got) != 1 || got[0] != "T1" {
		t.Fatalf("promoted %v, want the blocker T1", got)
	}
	// The design dispatcher never had a pause gate of its own — it had
	// nothing to dispatch. Assert it picks the ticket up now that it does,
	// or this proves only that a label moved.
	blocker.State = protocol.ReadyForDesign
	blocker.Last = &Transition{From: protocol.Todo, To: protocol.ReadyForDesign, Actor: RoleControlPlane, At: blocker.StateSince}
	s = snap(b, blocker)
	if a := find(Sweep(s), ActDispatch, "T1"); a == nil || a.Agent != AgentDesign {
		t.Errorf("no design run dispatched for the promoted blocker: %v", Sweep(s))
	}
}

// Without a blocker among them the pause is the reason, and it has to be
// the one reported. Counting these as blocked or as milestone-less names a
// condition the author could go and fix and would still leave nothing
// moving — which is how ORC-156 stayed stuck without the report saying so.
func TestTheReportNamesThePauseHoldingTicketsBack(t *testing.T) {
	b := tk("B1", protocol.Todo, func(t *Ticket) { t.Labels = []string{LabelBoundary} })
	s := snap(b, tk("T1", protocol.Todo))
	if got := promotions(s); len(got) != 0 {
		t.Errorf("promoted %v during the pause with no blocker among them", got)
	}
	p := NextPromotion(s)
	if !strings.Contains(p.Why, "pause") || !strings.Contains(p.Why, "B1") {
		t.Errorf("want the pause and the boundary ticket named, got %q", p.Why)
	}
}

func TestBoundaryAndAuthorOnlyAreNeverPromoted(t *testing.T) {
	// The boundary ticket's own Todo is the author's manual pass, and it
	// would pause the queue anyway — checked here so that removing the
	// pause would not silently start promoting it.
	if got := promotions(snap(tk("B1", protocol.Todo, func(t *Ticket) { t.Labels = []string{LabelBoundary} }))); len(got) != 0 {
		t.Errorf("promoted the boundary ticket: %v", got)
	}
	if got := promotions(snap(tk("A1", protocol.Todo, func(t *Ticket) { t.Labels = []string{LabelAuthorOnly} }))); len(got) != 0 {
		t.Errorf("promoted an author-only ticket: %v", got)
	}
}

// An author-only ticket parked in the design queue must not count as
// occupancy. The design dispatcher skips it (DESIGN §5), so it would sit
// there for as long as the author left it — and counting it would stop
// every promotion on the project for that whole time.
func TestAnAuthorOnlyTicketInTheQueueDoesNotDeadlockPromotion(t *testing.T) {
	s := snap(
		tk("A1", protocol.ReadyForDesign, func(t *Ticket) { t.Labels = []string{LabelAuthorOnly} }),
		tk("T1", protocol.Todo),
	)
	if got := promotions(s); len(got) != 1 || got[0] != "T1" {
		t.Errorf("promoted %v, want T1 — an author-only ticket is not queue occupancy", got)
	}
}

// The report stopped being advice the moment the sweep started acting on
// the same derivation. One answer, read twice: if these two disagree the
// report reads correctly and is wrong, which is worse than saying nothing.
func TestTheReportNamesWhatTheSweepWillPromote(t *testing.T) {
	s := snap(tk("T2", protocol.Todo), tk("T1", protocol.Todo))
	o := ComputeOrder(s, "")
	if o.Next.Ticket == nil {
		t.Fatalf("the report promotes nothing: %q", o.Next.Why)
	}
	got := promotions(s)
	if len(got) != 1 || got[0] != o.Next.Ticket.ID {
		t.Errorf("the sweep promotes %v, the report says %s", got, o.Next.Ticket.Key)
	}
}

func TestTheReportExplainsPromotingNothing(t *testing.T) {
	s := snap(
		tk("B", protocol.InProgress),
		tk("T1", protocol.Todo, func(t *Ticket) { t.BlockedBy = []string{"B"} }),
		tk("T2", protocol.Todo, func(t *Ticket) { t.Milestone = "" }),
		tk("T3", protocol.Todo, func(t *Ticket) { t.Milestone = "M2" }),
	)
	p := ComputeOrder(s, "").Next
	if p.Ticket != nil {
		t.Fatalf("promoted %s, want nothing", p.Ticket.Key)
	}
	for _, want := range []string{"3 waiting", "1 blocked", "1 carrying no milestone", "1 in a later milestone"} {
		if !strings.Contains(p.Why, want) {
			t.Errorf("reason %q is missing %q", p.Why, want)
		}
	}
}

// The one case where the report's threshold and the sweep's differ. The
// ticket stays in "freed by what is in flight" — moving it would be wrong
// about dev pickup — so the flag is what carries the difference.
func TestMergedBlockersAreFlaggedWithoutMovingTheTicket(t *testing.T) {
	s := snap(
		tk("B", protocol.Merged),
		tk("T1", protocol.Todo, func(t *Ticket) { t.BlockedBy = []string{"B"} }),
		tk("T2", protocol.Todo),
	)
	o := ComputeOrder(s, "")
	var found bool
	for _, l := range o.Layers {
		for _, ot := range l.Tickets {
			switch ot.Key {
			case "T1":
				found = true
				if l.Name != LayerAfterFlight {
					t.Errorf("T1 is in %q, want it left in %q", l.Name, LayerAfterFlight)
				}
				if !ot.DesignCanStart {
					t.Error("a ticket whose blockers are all merged must say design can start")
				}
			case "T2":
				// Nothing outstanding, so the flag would be noise: it
				// would print on every ticket in "ready now".
				if ot.DesignCanStart {
					t.Error("an unblocked ticket must not carry the merged-blocker note")
				}
			}
		}
	}
	if !found {
		t.Fatal("T1 is absent from the report")
	}
}

func TestKillSwitchPromotesNothing(t *testing.T) {
	s := snap(tk("T1", protocol.Todo))
	s.KillSwitch = true
	if p := NextPromotion(s); p.Ticket != nil || !strings.Contains(p.Why, "kill switch") {
		t.Errorf("want the kill switch named and nothing promoted, got %+v", p)
	}
}
