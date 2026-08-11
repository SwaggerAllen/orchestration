package core

import (
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// wantsAuthor reports whether this state hands the ball to the author
// (DESIGN §3). It is the state table's "who has it" column, read as a
// predicate — so a state added to that column must be added here, and the
// two cannot drift without a test noticing.
func wantsAuthor(t *Ticket) bool {
	switch t.State {
	case protocol.DesignReview, protocol.Blocked, protocol.BoundaryReview:
		return true
	case protocol.Todo:
		// The boundary ticket's Todo IS the author's manual pass
		// (DESIGN §10). Every other Todo is nobody's.
		return t.IsBoundary()
	}
	return false
}

// assignmentFor drives the tracker's assignee from state. Emitted only on
// a mismatch, so a settled ticket plans nothing and the sweep still
// converges. An empty AuthorID turns the whole mechanism off rather than
// unassigning everything — a project without the mapping keeps whatever a
// human set, which is the safer reading of missing config.
func assignmentFor(s *Snapshot, t *Ticket) []Action {
	if s.AuthorID == "" {
		return nil
	}
	want := ""
	reason := "no state hands the author the ball (DESIGN §3)"
	if wantsAuthor(t) {
		want = s.AuthorID
		reason = "state " + string(t.State) + " hands the author the ball (DESIGN §3)"
	}
	if t.AssigneeID == want {
		return nil
	}
	return []Action{{
		Kind:     ActAssign,
		TicketID: t.ID,
		Assignee: want,
		Reason:   reason,
	}}
}
