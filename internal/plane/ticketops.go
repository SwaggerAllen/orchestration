package plane

import (
	"context"

	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// TransitionTicket moves one ticket, resolving the protocol state through
// the config's name table. The agent harness uses this for claims and
// hand-offs; sweep actions go through Execute instead.
func (p *Plane) TransitionTicket(ctx context.Context, ticketID string, to protocol.State) error {
	if err := p.resolveStates(ctx); err != nil {
		return err
	}
	return p.Tracker.UpdateIssueState(ctx, ticketID, p.idByState[to])
}

// CommentTicket posts one comment body verbatim.
func (p *Plane) CommentTicket(ctx context.Context, ticketID, body string) error {
	return p.Tracker.CommentOnIssue(ctx, ticketID, body)
}

// AddTicketLabel attaches one label by name.
func (p *Plane) AddTicketLabel(ctx context.Context, ticketID, label string) error {
	return p.Tracker.AddIssueLabel(ctx, p.Config.Tracker.TeamID, ticketID, label)
}

// PRForTicket finds the open PR carrying the ticket key in its branch
// name, or nil — including when no host is attached.
func (p *Plane) PRForTicket(ctx context.Context, ticketKey string) *host.PR {
	if p.Host == nil {
		return nil
	}
	prs, err := p.Host.ListOpenPRs(ctx)
	if err != nil {
		return nil
	}
	return prForTicket(prs, ticketKey)
}
