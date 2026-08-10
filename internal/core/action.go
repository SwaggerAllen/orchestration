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
	// ActDispatch starts an agent run for a ticket.
	ActDispatch ActionKind = "dispatch"
	// ActCreateBoundary creates the milestone boundary ticket (DESIGN §10).
	ActCreateBoundary ActionKind = "create-boundary"
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
	Agent     AgentKind      // ActDispatch
	Milestone string         // ActCreateBoundary
	Reason    string
}

func (a Action) String() string {
	switch a.Kind {
	case ActTransition:
		return fmt.Sprintf("transition %s -> %s (%s)", a.TicketID, a.To, a.Reason)
	case ActComment:
		return fmt.Sprintf("comment %s %s (%s)", a.TicketID, a.Marker.Kind, a.Reason)
	case ActRemoveLabel:
		return fmt.Sprintf("remove-label %s %q (%s)", a.TicketID, a.Label, a.Reason)
	case ActDispatch:
		return fmt.Sprintf("dispatch %s agent for %s (%s)", a.Agent, a.TicketID, a.Reason)
	case ActCreateBoundary:
		return fmt.Sprintf("create boundary ticket for milestone %q (%s)", a.Milestone, a.Reason)
	}
	return fmt.Sprintf("unknown action %q", a.Kind)
}
