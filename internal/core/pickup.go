package core

import (
	"fmt"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// VerifyPickup is the agent-side half of enforcement: the claim assertion
// (DESIGN §6, §9). Tracker enforcement is detect-and-revert, so an agent
// can be dispatched against a state the control plane is about to undo;
// refusing to act when these checks fail is what closes that window. It
// mirrors the sweep's dispatch conditions — a pickup the sweep would not
// have planned is one the agent must not perform.
func VerifyPickup(s *Snapshot, ticketID string, kind AgentKind) error {
	t := s.ticket(ticketID)
	if t == nil {
		return fmt.Errorf("pickup: no ticket %q in the project scope — the project filter is part of the queue (DESIGN §2)", ticketID)
	}
	if s.KillSwitch {
		return fmt.Errorf("pickup %s: kill switch is on", t.Key)
	}
	// Author-only work is not the pipeline's, whatever state it is in
	// (DESIGN §8). The sweep already skips these when it dispatches;
	// this is the half that holds when a run arrives some other way — a
	// hand-fired workflow, or a dispatch planned in the beat before the
	// label was applied.
	if t.HasLabel(LabelAuthorOnly) {
		return fmt.Errorf("pickup %s: labelled %s — the author owns this one end to end (DESIGN §8)", t.Key, LabelAuthorOnly)
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
	// This narrows the window rather than closing it: a second run now
	// aborts in seconds instead of finishing. Closing it wants a
	// reservation written by the sweep at dispatch — by the thing that
	// decides, so the record exists before the next sweep can read it —
	// which the move record now gives us somewhere to put.
	if other := liveRunOfKind(s, t.ID, kind); other != nil {
		return fmt.Errorf("pickup %s: %s agent already running on %s (run %s) — one agent of a kind at a time (DESIGN §6)",
			t.Key, kind, other.Key, other.Run.ID)
	}

	switch kind {
	case AgentDev:
		if t.State != protocol.ReadyForDev && t.State != protocol.ReadyForRework {
			return fmt.Errorf("pickup %s: state is %q, dev picks up from the queues only", t.Key, t.State)
		}
		if t.HasLabel(LabelBoundary) {
			return fmt.Errorf("pickup %s: milestone-boundary tickets are not the dev agent's (DESIGN §10)", t.Key)
		}
		if t.HasLabel(LabelReEvaluate) {
			return fmt.Errorf("pickup %s: re-evaluate blocks pickup until design clears it (DESIGN §7)", t.Key)
		}
		if other, mine := MutexHolder(s, t); other != nil {
			return fmt.Errorf("pickup %s: mutex label %q already in flight on %s (DESIGN §6)", t.Key, mine, other.Key)
		}
		if blockedByOpen(s, t) {
			return fmt.Errorf("pickup %s: blocked by an open ticket (DESIGN §9)", t.Key)
		}
		if s.Paused() {
			b := s.boundaryTicket()
			if !t.Urgent() && (b == nil || !blocks(t, b.ID)) {
				return fmt.Errorf("pickup %s: queue is paused for the milestone boundary; only blockers and Urgent run (DESIGN §10)", t.Key)
			}
		}
	case AgentDesign:
		if t.State != protocol.Designing &&
			!((t.State == protocol.ReadyForDev || t.State == protocol.ReadyForRework) && t.HasLabel(LabelReEvaluate)) {
			return fmt.Errorf("pickup %s: design runs on Designing tickets or re-evaluate re-reads, not %q", t.Key, t.State)
		}
	case AgentReconcile:
		if t.State != protocol.Reconciling {
			return fmt.Errorf("pickup %s: reconcile runs on Reconciling tickets, not %q", t.Key, t.State)
		}
	case AgentBoundary:
		if !t.IsBoundary() || t.State != protocol.InProgress {
			return fmt.Errorf("pickup %s: boundary agent runs on the boundary ticket in In progress only (DESIGN §10)", t.Key)
		}
	default:
		return fmt.Errorf("pickup %s: unknown agent kind %q", t.Key, kind)
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
