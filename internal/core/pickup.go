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
		for _, other := range s.Tickets {
			if other.ID == t.ID || !other.InFlight() {
				continue
			}
			for _, mine := range t.MutexLabels() {
				if other.HasLabel(mine) {
					return fmt.Errorf("pickup %s: mutex label %q already in flight on %s (DESIGN §6)", t.Key, mine, other.Key)
				}
			}
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
