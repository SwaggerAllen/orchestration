package core

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Promotion is what the sweep will move from `Todo` into the design
// queue, or the reason it will move nothing (DESIGN §8).
//
// One answer, computed once and read twice: the sweep acts on it and
// `pipeline order` prints it. They have to agree. The report was advice
// while a human performed the move it derived — a reader who disagreed
// simply did something else — and it stopped being advice the moment the
// control plane began acting on the same derivation. Two implementations
// of "what is next" would drift, and the drift would be invisible: the
// report would read correctly and be wrong, on exactly the days somebody
// was relying on it to know what the pipeline was about to do.
type Promotion struct {
	// Ticket is what moves, or nil when nothing does.
	Ticket *Ticket
	// Why explains a nil Ticket, as a sentence the report can print
	// without rewriting. It is prose because the reasons are not a
	// closed set worth enumerating for a caller — every one of them ends
	// as a line a human reads, and the useful part is the specifics: the
	// key occupying the queue, the counts held back.
	Why string
}

// NextPromotion answers "what enters the design queue next" from settled
// state, which is what the report wants: it predicts the next sweep.
func NextPromotion(s *Snapshot) Promotion { return nextPromotion(s, nil) }

// nextPromotion takes the set of tickets a sweep is already moving. The
// sweep must skip them for the same reason dispatch does — a promotion
// racing a transition this pass has planned but not yet made would act
// on a state that is about to change.
func nextPromotion(s *Snapshot, skip map[string]bool) Promotion {
	if s.KillSwitch {
		return Promotion{Why: "the kill switch is set, so the pipeline writes nothing (DESIGN §13)"}
	}
	if s.Paused() {
		// Absolute here, Urgent included, and stricter than dev pickup
		// on purpose. The pause exists so a milestone's scope stops
		// changing while it is audited, and a design pass is the one
		// thing that adds to that scope — new artifacts, new mutex
		// labels, in the middle of the archive and the debt scan. Dev
		// under Urgent is finishing work already designed; this would be
		// starting work that is not (DESIGN §8, §10).
		return Promotion{Why: fmt.Sprintf(
			"the queue is paused — %s is open, and a design pass would add to the milestone being audited",
			s.boundaryTicket().Key)}
	}
	if held := queueHolder(s); held != nil {
		return Promotion{Why: fmt.Sprintf(
			"%s is already in Ready for design; the queue is kept one deep so that its order is the order work runs",
			held.Key)}
	}

	ordered := make([]*Ticket, 0, len(s.Tickets))
	for _, t := range s.Tickets {
		if !skip[t.ID] {
			ordered = append(ordered, t)
		}
	}
	// The precedence rule, which is what the report sorts each layer by —
	// and every candidate here is in Todo, so it resolves to urgent
	// first, then oldest, then key. Nothing new is computed for this.
	sort.Slice(ordered, func(i, j int) bool { return Precedes(ordered[i], ordered[j]) })
	for _, t := range ordered {
		if promotable(s, t) {
			return Promotion{Ticket: t}
		}
	}
	return Promotion{Why: heldBack(s, ordered)}
}

// queueHolder returns the ticket occupying the design queue, if the
// pipeline would ever move it on.
//
// An `author-only` ticket parked in the queue is skipped, and that is not
// tidiness: the design dispatcher skips it too (DESIGN §5), so it would
// sit there forever, and counting it as occupancy would stop every
// promotion on the project for as long as the author left it. Same for
// the boundary ticket, which has its own state meanings.
func queueHolder(s *Snapshot) *Ticket {
	for _, t := range s.Tickets {
		if t.State != protocol.ReadyForDesign {
			continue
		}
		if t.IsBoundary() || t.HasLabel(LabelAuthorOnly) {
			continue
		}
		return t
	}
	return nil
}

// promotable reports the per-ticket half of the rule. The queue-wide
// conditions — the pause, the queue's depth — are not facts about a
// ticket and are checked once, above.
func promotable(s *Snapshot, t *Ticket) bool {
	if t.State != protocol.Todo {
		return false
	}
	if t.IsBoundary() || t.HasLabel(LabelAuthorOnly) {
		return false
	}
	// Milestone-less is held back rather than promoted, and it is the
	// condition most likely to look like a bug from outside. A ticket in
	// Todo with no milestone is startable but uncommitted, and assigning
	// the milestone is the commitment (DESIGN §10) — so promoting one
	// commits work as a side effect, at the exact moment the two cases
	// are indistinguishable: a proposal accepted out of Triage with the
	// assignment still lagging looks identical to a ticket left
	// uncommitted on purpose. The report names both and says it cannot
	// tell them apart; this moves neither.
	//
	// Written as two clauses because one would not do it: with no
	// current milestone set, `t.Milestone != s.CurrentMilestone` is
	// false for every bare ticket and the whole backlog would promote.
	if t.Milestone == "" || t.Milestone != s.CurrentMilestone {
		return false
	}
	return !blockedBeforeMerge(s, t)
}

// blockedBeforeMerge reports whether any blocker has yet to reach `Merged`.
func blockedBeforeMerge(s *Snapshot, t *Ticket) bool {
	for _, id := range t.BlockedBy {
		if b := s.ticket(id); b != nil && !onMain(b) {
			return true
		}
	}
	return false
}

// onMain reports a blocker far enough along that a design pass on what it
// blocks can read its work.
//
// `Merged` rather than `Resolved` — the relaxation this rule exists for.
// A design pass reads `main`, and a merged blocker's work is on `main`;
// what remains of that ticket's life is the deploy and the post-deploy
// check, neither of which changes anything the pass would read.
//
// **The relaxation is design's alone.** `blockedByOpen`, which gates dev
// pickup, keeps `Resolved`, because a ticket that fails its post-deploy
// check goes to `Blocked` and code built on top of it would have to be
// re-examined, where a design that described it would only have to be
// re-read.
//
// `Blocked` is not on-main even for a ticket that merged before landing
// there. The state means a human owes a judgment, and one of the
// judgments available to them is a revert.
func onMain(t *Ticket) bool {
	return t.State == protocol.Merged || t.Resolved()
}

// heldBack explains a sweep that promotes nothing although the queue is
// free, by counting what is waiting and why.
//
// Counts rather than a list: the useful question is "is the pipeline
// stuck on me or on itself", and three numbers answer it where twelve
// keys would have to be read first.
func heldBack(s *Snapshot, ordered []*Ticket) string {
	var blocked, uncommitted, elsewhere, total int
	for _, t := range ordered {
		if t.State != protocol.Todo || t.IsBoundary() || t.HasLabel(LabelAuthorOnly) {
			continue
		}
		total++
		switch {
		case t.Milestone == "":
			uncommitted++
		case t.Milestone != s.CurrentMilestone:
			elsewhere++
		default:
			// Everything else about a current-milestone ticket in Todo is
			// already covered above, so what is left is the blocker graph.
			blocked++
		}
	}
	if total == 0 {
		return "nothing is waiting in Todo"
	}
	var parts []string
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked by work that has not reached Merged", blocked))
	}
	if uncommitted > 0 {
		parts = append(parts, fmt.Sprintf("%d carrying no milestone, which is yours to assign", uncommitted))
	}
	if elsewhere > 0 {
		parts = append(parts, fmt.Sprintf("%d in a later milestone", elsewhere))
	}
	return fmt.Sprintf("%d waiting in Todo, none of them promotable: %s", total, strings.Join(parts, "; "))
}
