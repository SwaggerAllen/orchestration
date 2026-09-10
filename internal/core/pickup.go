package core

import (
	"errors"
	"fmt"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// ErrRefused marks a pickup the protocol declined, as opposed to one the
// harness could not evaluate.
//
// The two look identical from a workflow — both are a claim command
// exiting non-zero — and they want opposite handling. A refusal is the
// pipeline working: the mutex is held, an agent of this kind is already
// running, a blocker is open. The ticket is exactly where it should be
// and a human needs no telling. A snapshot that could not be built is
// the harness broken, and the author has to hear about it.
//
// Catapult's ORC-7 is why this is a type rather than a convention: its
// claim died building the snapshot, which is the second kind, and the
// only thing distinguishing it from the first was prose in an error
// string. So the run was handled as neither — the ticket sat in a state
// meaning "an agent is working on this" for 23 minutes.
var ErrRefused = errors.New("pickup refused")

// Refused reports whether an error is the protocol declining a pickup.
func Refused(err error) bool { return errors.Is(err, ErrRefused) }

// refuse builds a refusal: the message a human reads, carrying the
// sentinel a caller routes on.
func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// VerifyPickup is the agent-side half of enforcement: the claim assertion
// (DESIGN §6, §9). Tracker enforcement is detect-and-revert, so an agent
// can be dispatched against a state the control plane is about to undo;
// refusing to act when these checks fail is what closes that window. It
// mirrors the sweep's dispatch conditions — a pickup the sweep would not
// have planned is one the agent must not perform.
func VerifyPickup(s *Snapshot, ticketID string, kind AgentKind, runID string) error {
	t := s.ticket(ticketID)
	if t == nil {
		return refuse("pickup: no ticket %q in the project scope — the project filter is part of the queue (DESIGN §2)", ticketID)
	}
	if s.KillSwitch {
		return refuse("pickup %s: kill switch is on", t.Key)
	}
	// Author-only work is not the pipeline's, whatever state it is in
	// (DESIGN §8). The sweep already skips these when it dispatches;
	// this is the half that holds when a run arrives some other way — a
	// hand-fired workflow, or a dispatch planned in the beat before the
	// label was applied.
	if t.HasLabel(LabelAuthorOnly) {
		return refuse("pickup %s: labelled %s — the author owns this one end to end (DESIGN §8)", t.Key, LabelAuthorOnly)
	}
	if t.HasLabel(LabelResync) {
		return refuse("pickup %s: labelled %s — the author is repairing this ticket's state by hand (DESIGN §9)", t.Key, LabelResync)
	}
	// Singularity, asked here because the dispatcher cannot answer it in
	// time. The sweep's guard reads Run.Live, and that marker is posted
	// by the claim — which happens inside the dispatched job, after
	// runner boot, checkout, toolchain and dependency install. Measured
	// on catapult: about ninety seconds between the dispatch and the
	// marker that proves it happened, and any sweep landing in that
	// window sees an idle agent and dispatches again. Two boundary
	// agents ran one ticket to completion that way — two full scans,
	// twenty-two minutes of model spend, and four tickets filed for two
	// findings.
	//
	// This narrows the window; the dispatch reservation closes it. The
	// sweep now claims the kind in the move store before it dispatches
	// (`plane.reserveDispatch`, DESIGN §6), so the record exists before
	// the next sweep can read it and the duplicate is never dispatched.
	//
	// Both halves stay, and neither is redundant. The reservation is a
	// write to a store that a project may not have configured and that
	// can be unreachable; this check reads a snapshot the run already
	// holds. Losing the reservation costs a duplicate dispatch, which
	// this refuses in seconds — which is the whole reason it was built
	// first.
	if other := liveRunOfKind(s, t.ID, kind); other != nil {
		return refuse("pickup %s: %s agent already running on %s (run %s) — one agent of a kind at a time (DESIGN §6)",
			t.Key, kind, other.Key, other.Run.ID)
	}
	// And the same question asked of this ticket, which the check above
	// cannot answer because it skips the ticket's own run — deliberately,
	// so a claim re-entering after a resume is not mistaken for a second
	// agent. The exclusion is by ticket, so two runs on one ticket each
	// looked at the other and saw themselves.
	//
	// That is what happened. ORC-45 was dispatched twice, 82 seconds
	// apart, and both runs scanned the tree, filed proposals and aborted
	// the ticket: about twenty-two minutes of duplicate model spend and
	// a duplicated set of tickets. Comparing run ids rather than tickets
	// keeps the resume case — a resumed claim carries the same id its
	// run was dispatched under — and refuses the second agent.
	if other := t.OtherLiveRun(kind, runID); other != nil {
		return refuse("pickup %s: %s run %s is already live on this ticket (this run is %s) — one agent of a kind at a time (DESIGN §6)",
			t.Key, kind, other.ID, runID)
	}

	switch kind {
	case AgentDev:
		if t.State != protocol.ReadyForDev && t.State != protocol.ReadyForRework {
			return refuse("pickup %s: state is %q, dev picks up from the queues only", t.Key, t.State)
		}
		if t.HasLabel(LabelBoundary) {
			return refuse("pickup %s: milestone-boundary tickets are not the dev agent's (DESIGN §10)", t.Key)
		}
		if t.HasLabel(LabelReEvaluate) {
			return refuse("pickup %s: re-evaluate blocks pickup until design clears it (DESIGN §7)", t.Key)
		}
		// MutexBlocker, matching the dispatcher exactly: this is the
		// same "may work start now" question, and the two answering it
		// differently is the failure the shared function exists to stop.
		if other, mine := MutexBlocker(s, t); other != nil {
			return refuse("pickup %s: mutex label %q already in flight on %s (DESIGN §6)", t.Key, mine, other.Key)
		}
		if blockedByOpen(s, t) {
			return refuse("pickup %s: blocked by an open ticket (DESIGN §9)", t.Key)
		}
		if s.Paused() {
			b := s.boundaryTicket()
			if !t.Urgent() && (b == nil || !blocks(t, b.ID)) {
				return refuse("pickup %s: queue is paused for the milestone boundary; only blockers and Urgent run (DESIGN §10)", t.Key)
			}
		}
	case AgentDesign:
		// Ready for design, not Designing: the claim is what writes
		// Designing, the same way dev's claim writes In progress. A
		// design run that finds the ticket already in Designing is a
		// second agent on work someone else claimed.
		if t.State != protocol.ReadyForDesign && t.State != protocol.ReadyForRedesign &&
			!((t.State == protocol.ReadyForDev || t.State == protocol.ReadyForRework) && t.HasLabel(LabelReEvaluate)) {
			return refuse("pickup %s: design runs on Ready for design or Ready for redesign tickets, or re-evaluate re-reads, not %q", t.Key, t.State)
		}
	case AgentReconcile:
		if t.State != protocol.Reconciling {
			return refuse("pickup %s: reconcile runs on Reconciling tickets, not %q", t.Key, t.State)
		}
	case AgentBoundary:
		if !t.IsBoundary() || t.State != protocol.InProgress {
			return refuse("pickup %s: boundary agent runs on the boundary ticket in In progress only (DESIGN §10)", t.Key)
		}
	default:
		return refuse("pickup %s: unknown agent kind %q", t.Key, kind)
	}
	return nil
}

// liveRunOfKind finds another ticket already holding a live run of this
// agent kind. The ticket's own run is excluded: a claim re-entering after
// a resume is the same run, not a second one.
func liveRunOfKind(s *Snapshot, ticketID string, kind AgentKind) *Ticket {
	for _, t := range s.Tickets {
		if t.ID == ticketID {
			continue
		}
		if t.LiveRun(kind) {
			return t
		}
	}
	return nil
}
