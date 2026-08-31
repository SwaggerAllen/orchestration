package core

import (
	"fmt"

	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// ActionKind is what a planned action does.
type ActionKind string

const (
	// ActTransition moves a ticket to a state, optionally posting a marker
	// comment in the same breath.
	ActTransition ActionKind = "transition"
	// ActComment posts a marker comment without a state change.
	ActComment ActionKind = "comment"
	// ActRemoveLabel removes one label.
	ActRemoveLabel ActionKind = "remove-label"
	// ActAssign sets the assignee ("" unassigns). Derived from state:
	// assignment mirrors who has the ball (DESIGN §3).
	ActAssign ActionKind = "assign"
	// ActDispatch starts an agent run for a ticket.
	ActDispatch ActionKind = "dispatch"
	// ActCreateBoundary creates the milestone boundary ticket (DESIGN §10).
	ActCreateBoundary ActionKind = "create-boundary"
	// ActAdopt writes the ticket's current state into the move record
	// without touching the tracker — the pipeline agreeing with where
	// the ticket already is, rather than moving it there.
	//
	// The only action that writes the record and nothing else. Every
	// other one records write-ahead as a consequence of a move it is
	// about to make, which is exactly what a ticket under repair must
	// not get: the author is moving it, and the record has to follow
	// rather than lead (DESIGN §9).
	ActAdopt ActionKind = "adopt"
)

// Action is one planned effect. The sweep returns these; adapters (or the
// sim's Apply) execute them. Reason is for humans — logs and --dry-run —
// and nothing parses it.
type Action struct {
	Kind      ActionKind
	TicketID  string
	To        protocol.State // ActTransition
	Marker    *marker.Marker // optional comment on transition; required for ActComment
	Prose     string         // human half of the comment
	Label     string         // ActRemoveLabel
	Assignee  string         // ActAssign; "" means unassign
	Agent     AgentKind      // ActDispatch
	Milestone string         // ActCreateBoundary
	Reason    string
}

func (a Action) String() string {
	switch a.Kind {
	case ActTransition:
		return fmt.Sprintf("transition %s -> %s (%s)", a.TicketID, a.To, a.Reason)
	case ActAdopt:
		return fmt.Sprintf("adopt %s at %s (%s)", a.TicketID, a.To, a.Reason)
	case ActComment:
		return fmt.Sprintf("comment %s %s (%s)", a.TicketID, a.Marker.Kind, a.Reason)
	case ActRemoveLabel:
		return fmt.Sprintf("remove-label %s %q (%s)", a.TicketID, a.Label, a.Reason)
	case ActAssign:
		if a.Assignee == "" {
			return fmt.Sprintf("unassign %s (%s)", a.TicketID, a.Reason)
		}
		return fmt.Sprintf("assign %s -> %s (%s)", a.TicketID, a.Assignee, a.Reason)
	case ActDispatch:
		return fmt.Sprintf("dispatch %s agent for %s (%s)", a.Agent, a.TicketID, a.Reason)
	case ActCreateBoundary:
		return fmt.Sprintf("create boundary ticket for milestone %q (%s)", a.Milestone, a.Reason)
	}
	return fmt.Sprintf("unknown action %q", a.Kind)
}
