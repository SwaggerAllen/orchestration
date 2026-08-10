package plane

import (
	"context"
	"fmt"
	"io"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// boundaryDescription is the boundary ticket's self-carried instructions
// (DESIGN §10): the state meanings are readable from the ticket rather
// than remembered.
const boundaryDescription = `This ticket is pipeline machinery. Automation created it and will not close it.

  Todo            → your pass. Manual test the milestone, and clear any Blocked
                    tickets labelled needs-review.
  In progress     → YOU move it here when your pass is done. This is the signal.
                    The boundary agent then runs archive / debt scan / grooming,
                    posting a comment per step. If it fails, move it back here and
                    it resumes from the first step with no comment.
  Boundary review → the agent put it back. Proposals are in Triage; accept or
                    decline, confirm the ranking, then close this ticket and pull
                    the next milestone into Todo.
  Done            → you close it. The queue resumes.

The queue is paused while this ticket is open. The dev agent will only pick up
tickets marked as blocking this one — plus anything marked Urgent, which
overrides the pause.

Blocking bug found during your pass?  File it against THIS milestone and mark it
blocking this ticket. Anything that can wait goes to Triage for the next milestone.`

// Execute applies sweep actions through the tracker port, logging each one
// to log — a mutating pass that doesn't say what it did can't be audited.
// Dispatch actions are logged and skipped until the agent workflows exist
// (M3); the sweep re-plans them every pass, so nothing is lost by skipping.
func (p *Plane) Execute(ctx context.Context, acts []core.Action, log io.Writer) error {
	if err := p.resolveStates(ctx); err != nil {
		return err
	}
	teamID := p.Config.Tracker.TeamID
	for _, a := range acts {
		fmt.Fprintln(log, "  "+a.String())
		switch a.Kind {
		case core.ActTransition:
			stateID, ok := p.idByState[a.To]
			if !ok {
				return fmt.Errorf("execute: no tracker state for %q", a.To)
			}
			if err := p.Tracker.UpdateIssueState(ctx, a.TicketID, stateID); err != nil {
				return fmt.Errorf("execute: %s: %w", a, err)
			}
			if body := commentBody(a); body != "" {
				if err := p.Tracker.CommentOnIssue(ctx, a.TicketID, body); err != nil {
					return fmt.Errorf("execute: %s: comment: %w", a, err)
				}
			}
		case core.ActComment:
			if err := p.Tracker.CommentOnIssue(ctx, a.TicketID, commentBody(a)); err != nil {
				return fmt.Errorf("execute: %s: %w", a, err)
			}
		case core.ActRemoveLabel:
			if err := p.Tracker.RemoveIssueLabel(ctx, teamID, a.TicketID, a.Label); err != nil {
				return fmt.Errorf("execute: %s: %w", a, err)
			}
		case core.ActDispatch:
			workflow := p.Config.Agents[string(a.Agent)]
			if p.Host == nil || workflow == "" {
				fmt.Fprintf(log, "    (dispatch skipped: %s agent not wired — no host or no workflow in config.agents)\n", a.Agent)
				continue
			}
			key, ok := p.keyByID[a.TicketID]
			if !ok {
				return fmt.Errorf("execute: %s: no key for ticket id %s — Execute must follow Build", a, a.TicketID)
			}
			if err := p.Host.DispatchWorkflow(ctx, workflow, map[string]string{"ticket": key}); err != nil {
				return fmt.Errorf("execute: %s: %w", a, err)
			}
		case core.ActCreateBoundary:
			if err := p.createBoundary(ctx, a); err != nil {
				return fmt.Errorf("execute: %s: %w", a, err)
			}
		default:
			return fmt.Errorf("execute: unknown action kind %q", a.Kind)
		}
	}
	return nil
}

func commentBody(a core.Action) string {
	if a.Marker != nil {
		return a.Marker.Comment(a.Prose)
	}
	return a.Prose
}

func (p *Plane) createBoundary(ctx context.Context, a core.Action) error {
	milestones, err := p.Tracker.ListMilestones(ctx, p.Config.Tracker.ProjectID)
	if err != nil {
		return err
	}
	milestoneID := ""
	for _, m := range milestones {
		if m.Name == a.Milestone {
			milestoneID = m.ID
		}
	}
	if milestoneID == "" {
		return fmt.Errorf("no milestone named %q in the project", a.Milestone)
	}
	desc := boundaryDescription
	if a.Prose != "" {
		desc = a.Prose + "\n\n" + desc
	}
	_, err = p.Tracker.CreateIssue(ctx, tracker.NewIssue{
		TeamID:      p.Config.Tracker.TeamID,
		ProjectID:   p.Config.Tracker.ProjectID,
		MilestoneID: milestoneID,
		Title:       "Milestone boundary — " + a.Milestone,
		Description: desc,
		StateID:     p.idByState[protocol.Todo],
		Labels:      []string{core.LabelBoundary},
	})
	return err
}
