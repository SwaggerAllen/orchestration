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

// AddTicketLabel attaches one label by name, creating it if the team
// lacks it.
//
// It used to attach only, on the theory that the fixed set (protocol
// .Labels) is provisioned by setup and the per-name mutex labels are the
// only ones born late. That theory breaks every time the protocol gains
// a label: setup provisions it, but only on projects where somebody
// re-runs setup, and until they do the attach fails — so an agent
// aborting with needs-setup could not file the abort, which is the worst
// possible moment to discover a label is missing. Creating on demand
// makes the two paths one, and a label the team already has costs one
// list call.
func (p *Plane) AddTicketLabel(ctx context.Context, ticketID, label string) error {
	return p.ensureLabel(ctx, ticketID, label)
}

// RemoveTicketLabel detaches one label by name.
func (p *Plane) RemoveTicketLabel(ctx context.Context, ticketID, label string) error {
	return p.Tracker.RemoveIssueLabel(ctx, p.Config.Tracker.TeamID, ticketID, label)
}

// EnsureMutexLabel attaches a screen:<name> or system:<name> label.
// Named separately from AddTicketLabel because the call sites read
// differently — mutex labels are born per-name as design discovers them
// (DESIGN §6, §8) — but the behaviour is now the same for both.
func (p *Plane) EnsureMutexLabel(ctx context.Context, ticketID, label string) error {
	return p.ensureLabel(ctx, ticketID, label)
}

func (p *Plane) ensureLabel(ctx context.Context, ticketID, label string) error {
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
	switch kind {
	case "design":
		label = "design-inbox"
	case "harness":
		// The pipeline's own problems, filed where the author triages
		// everything else but labelled apart: "is the harness costing
		// us tickets" is a different question from "is this codebase
		// accruing debt", and one list cannot answer both.
		label = "harness"
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
