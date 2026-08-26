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

  Todo            → your pass. The live suite's result lands below as a comment —
                    read it first; a failure becomes a blocker like any finding.
                    Manual test the milestone, and clear any Blocked tickets
                    labelled needs-review.
  In progress     → YOU move it here when your pass is done. This is the signal.
                    The boundary agent then runs archive / debt scan / grooming,
                    posting a comment per step. If it fails, move it back here and
                    it resumes from the first step with no comment. Once it has
                    finished a pass, moving it back here starts a NEW one — the
                    scan runs again over whatever has landed since.
  Boundary review → the agent put it back. Proposals are in Triage; accept or
                    decline, confirm the ranking, then close this ticket and pull
                    the next milestone into Todo. The comment above proposes the
                    next debt milestone's contents — assigning those milestones is
                    yours, because assigning one commits the work.
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
			// Write-ahead. The record goes down before the move, and a
			// failure here stops the move rather than proceeding without
			// it: an unrecorded transition reads as a human's on the next
			// sweep, gets reverted, re-made and reverted again. The sweep
			// is convergent, so declining costs a beat.
			if err := p.record(ctx, a.TicketID, a.To, core.RoleControlPlane); err != nil {
				return fmt.Errorf("execute: %s: recording the move before making it: %w", a, err)
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
		case core.ActAssign:
			if err := p.Tracker.AssignIssue(ctx, a.TicketID, a.Assignee); err != nil {
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
			// The reservation, taken by the thing that decides, before
			// it acts (DESIGN §6). Everything else that asks "is this
			// agent kind busy" reads the run list, and a run does not
			// appear there the instant it is dispatched — measured on
			// catapult at about ninety seconds from dispatch to the
			// proof it happened. Every sweep landing in that window saw
			// an idle agent and dispatched again, which is how two
			// boundary agents ran ORC-45 to completion.
			//
			// core.VerifyPickup already aborts the loser in seconds
			// rather than letting it finish. This is the other half: the
			// record exists before the next sweep can read it, so the
			// duplicate is never dispatched at all.
			held, err := p.reserveDispatch(ctx, a.Agent, a.TicketID)
			if err != nil {
				// Fail closed, exactly as the move record does: a
				// dispatch we cannot record the intent of is one we
				// cannot tell apart from a duplicate next beat. The
				// sweep is convergent, so declining costs a beat.
				return fmt.Errorf("execute: %s: reserving the %s agent before dispatching it: %w", a, a.Agent, err)
			}
			if held != "" {
				fmt.Fprintf(log, "    (dispatch skipped: the %s agent is reserved by %s — one agent of a kind at a time)\n", a.Agent, p.keyOf(held))
				continue
			}
			if err := p.Host.DispatchWorkflow(ctx, workflow, map[string]string{"ticket": key}); err != nil {
				// Hand the reservation back. A dispatch that never
				// happened must not hold the kind for the whole TTL —
				// the next beat should be free to try again, and a
				// transient host error is exactly the case that would
				// otherwise idle an agent for five minutes.
				p.releaseDispatch(ctx, a.Agent, a.TicketID)
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

// record writes down a move before it is made. The `from` is read from
// the snapshot the plane built, because the record is an edge and the
// writer matrix judges edges — "arrived at Ready for dev" is not a rule,
// "arrived at Ready for dev from Designing" is.
//
// No store configured is not an error. A project that has not been wired
// up records nothing and is judged on nothing, which is the same safe
// place a brand-new ticket sits in.
func (p *Plane) record(ctx context.Context, ticketID string, to protocol.State, role core.Role) error {
	if p.State == nil {
		return nil
	}
	return p.State.Record(ctx, ticketID, core.RecordedMove{
		From: p.stateOf[ticketID],
		To:   to,
		Role: role,
	})
}
