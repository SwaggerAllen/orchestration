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

// RemoveTicketLabel detaches one label by name.
func (p *Plane) RemoveTicketLabel(ctx context.Context, ticketID, label string) error {
	return p.Tracker.RemoveIssueLabel(ctx, p.Config.Tracker.TeamID, ticketID, label)
}

// EnsureScreenLabel creates the screen:<name> label if the team lacks it,
// then attaches it. Screen labels are born per-screen as design discovers
// them (DESIGN §6, §8), so unlike the fixed set they are created on
// demand.
func (p *Plane) EnsureScreenLabel(ctx context.Context, ticketID, label string) error {
	teamID := p.Config.Tracker.TeamID
	labels, err := p.Tracker.ListLabels(ctx, teamID)
	if err != nil {
		return err
	}
	exists := false
	for _, l := range labels {
		if l.Name == label {
			exists = true
		}
	}
	if !exists {
		if _, err := p.Tracker.CreateLabel(ctx, teamID, label); err != nil {
			return err
		}
	}
	return p.Tracker.AddIssueLabel(ctx, teamID, ticketID, label)
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
