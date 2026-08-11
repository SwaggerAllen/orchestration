package plane

import (
	"context"
	"fmt"
	"sort"

	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
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

// EnsureMutexLabel creates a screen:<name> or system:<name> label if the
// team lacks it, then attaches it. Mutex labels are born per-name as
// design discovers them (DESIGN §6, §8), so unlike the fixed set they are
// created on demand.
func (p *Plane) EnsureMutexLabel(ctx context.Context, ticketID, label string) error {
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

// Milestones returns the project's milestones in the tracker's own order.
// Milestones are queried, never configured: the tracker is canonical for
// scope (DESIGN §1, §5).
func (p *Plane) Milestones(ctx context.Context) ([]tracker.Milestone, error) {
	ms, err := p.Tracker.ListMilestones(ctx, p.Config.Tracker.ProjectID)
	if err != nil {
		return nil, err
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].SortOrder < ms[j].SortOrder })
	return ms, nil
}

// StateIDFor resolves a protocol state to the team's state id.
func (p *Plane) StateIDFor(ctx context.Context, s protocol.State) (string, error) {
	if err := p.resolveStates(ctx); err != nil {
		return "", err
	}
	return p.idByState[s], nil
}

// FileTriageProposal creates one boundary proposal issue. It lands in the
// team's Triage state when one exists, Backlog otherwise — Triage is
// Linear-managed, so setup can't guarantee it. The dedupe marker rides in
// the description; a re-run checks it before filing (DESIGN §10).
func (p *Plane) FileTriageProposal(ctx context.Context, title, description, kind string, gating bool, dedupe string) error {
	if err := p.resolveStates(ctx); err != nil {
		return err
	}
	states, err := p.Tracker.ListStates(ctx, p.Config.Tracker.TeamID)
	if err != nil {
		return err
	}
	stateID := p.idByState[protocol.Backlog]
	for _, s := range states {
		if s.Category == protocol.CategoryTriage {
			stateID = s.ID
		}
	}
	label := "tech-debt"
	if kind == "design" {
		label = "design-inbox"
	}
	m := marker.Marker{Kind: marker.TriageProposal, Fields: map[string]string{
		"dedupe": dedupe,
		"gating": fmt.Sprintf("%t", gating),
	}}
	_, err = p.Tracker.CreateIssue(ctx, tracker.NewIssue{
		TeamID:      p.Config.Tracker.TeamID,
		ProjectID:   p.Config.Tracker.ProjectID,
		Title:       title,
		Description: m.Format() + "\n\n" + description,
		StateID:     stateID,
		Labels:      []string{label},
	})
	return err
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
