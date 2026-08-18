package plane

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/filemap"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// TransitionTicket moves one ticket, resolving the protocol state through
// the config's name table. The agent harness uses this for claims and
// hand-offs; sweep actions go through Execute instead.
// The role is a parameter rather than a field on the Plane so that no
// caller can move a ticket without saying who is moving it. Every
// transition the pipeline makes is recorded under a role, and the
// writer matrix judges roles — a move recorded as nobody is a move the
// next sweep reads as the author's and reverts.
func (p *Plane) TransitionTicket(ctx context.Context, ticketID string, to protocol.State, role core.Role) error {
	if err := p.resolveStates(ctx); err != nil {
		return err
	}
	// Write-ahead, and fatal if it fails: see (*Plane).record.
	if err := p.record(ctx, ticketID, to, role); err != nil {
		return fmt.Errorf("transition %s: recording the move before making it: %w", ticketID, err)
	}
	if err := p.Tracker.UpdateIssueState(ctx, ticketID, p.idByState[to]); err != nil {
		return err
	}
	// So a second transition in the same process records the right edge
	// rather than the one the snapshot was built with.
	if p.stateOf != nil {
		p.stateOf[ticketID] = to
	}
	return nil
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
func (p *Plane) FileTriageProposal(ctx context.Context, title, description, kind, subject string, gating bool, dedupe string) error {
	if err := p.resolveStates(ctx); err != nil {
		return err
	}
	stateID, err := p.triageStateID(ctx)
	if err != nil {
		return err
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
	issue, err := p.Tracker.CreateIssue(ctx, tracker.NewIssue{
		TeamID:      p.Config.Tracker.TeamID,
		ProjectID:   p.Config.Tracker.ProjectID,
		Title:       title,
		Description: m.Format() + "\n\n" + description,
		StateID:     stateID,
		Labels:      []string{label},
	})
	if err != nil {
		return err
	}
	// The subject names the concrete thing the proposal is about — a
	// file, a config key, a gate line — so when it names a path no agent
	// can land a change to, that is knowable here rather than being left
	// for whoever reads the ticket later. A proposal to change a
	// workflow file dispatched to the dev agent is a run that ends on a
	// rejected push, every time, because the push token has no workflow
	// scope (DESIGN §5).
	//
	// Attached after the create rather than passed into it: creating an
	// issue with a label the team does not carry fails the whole create,
	// while AddTicketLabel provisions a missing label on the way through.
	// The proposal landing under-labelled is recoverable; the proposal
	// not landing is not.
	if AuthorOnly(subject) {
		if err := p.AddTicketLabel(ctx, issue.ID, core.LabelAuthorOnly); err != nil {
			return fmt.Errorf("filed %s but could not mark it %s (its subject %q is author-only): %w",
				issue.Key, core.LabelAuthorOnly, subject, err)
		}
	}
	return nil
}

// AuthorOnly reports whether a proposal subject names a path only the
// author can change (protocol.AuthorOnlyPaths).
//
// Subjects are free text — a path, a config key, a mix task, a module —
// so this is a best-effort read of one that happens to be a path, and it
// is deliberately one-directional: a match is reliable, a non-match only
// means this subject did not name one. The dev agent's own outcome is
// the other half, for the case discovered while working rather than
// while proposing.
func AuthorOnly(subject string) bool {
	subject = strings.TrimSpace(strings.Trim(strings.TrimSpace(subject), "`"))
	if subject == "" {
		return false
	}
	for _, g := range protocol.AuthorOnlyPaths {
		if filemap.Match(g, subject) {
			return true
		}
	}
	return false
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

// TriageProposal is a proposal already sitting in Triage.
type TriageProposal struct {
	Key    string
	Title  string
	Dedupe string
}

// ListTriageProposals reads the proposals already filed, straight from
// the tracker rather than from a snapshot.
//
// It has to bypass the snapshot, and the reason is the whole bug. Build
// skips tickets in a triage-category state — deliberately, because the
// protocol does not map them — and FileTriageProposal files into exactly
// those states. So a dedupe set assembled from snap.Tickets could never
// contain a filed proposal: the check was not weak, it was inert, and
// two identical keys would have produced two tickets just as readily as
// two different ones did.
//
// It does not filter by state either, and that is the second half of the
// same lesson. Filtering to triage-category states made the dedupe set
// "proposals nobody has looked at yet": the moment the author accepted
// one and moved it into the queue it dropped out, and the next scan that
// found the same thing filed it again. Harmless while a milestone had
// exactly one boundary pass, and not once a second pass became ordinary.
// A proposal's marker is the record that it was filed, wherever the
// ticket has since travelled.
func (p *Plane) ListTriageProposals(ctx context.Context) ([]TriageProposal, error) {
	if err := p.resolveStates(ctx); err != nil {
		return nil, err
	}
	issues, err := p.Tracker.ListIssues(ctx, p.Config.Tracker.TeamID, p.Config.Tracker.ProjectID)
	if err != nil {
		return nil, err
	}
	var out []TriageProposal
	for _, i := range issues {
		tp := TriageProposal{Key: i.Key, Title: i.Title}
		for _, line := range strings.Split(i.Description, "\n") {
			m, ok, err := marker.Parse(line)
			if err == nil && ok && m.Kind == marker.TriageProposal {
				tp.Dedupe = m.Fields["dedupe"]
			}
		}
		if tp.Dedupe == "" {
			// Not a filed proposal — now that every issue is considered,
			// the marker is what identifies one.
			continue
		}
		out = append(out, tp)
	}
	return out, nil
}

// triageStateID resolves where a filed proposal lands: the team's Triage
// state when one exists, Backlog otherwise. Triage is Linear-managed, so
// setup cannot guarantee it (DESIGN §10). Callers must have resolved
// states first.
func (p *Plane) triageStateID(ctx context.Context) (string, error) {
	states, err := p.Tracker.ListStates(ctx, p.Config.Tracker.TeamID)
	if err != nil {
		return "", err
	}
	stateID := p.idByState[protocol.Backlog]
	for _, s := range states {
		if s.Category == protocol.CategoryTriage {
			stateID = s.ID
		}
	}
	return stateID, nil
}

// FileAuthorOnlyBlocker is the dev run's escape hatch (DESIGN §8, §12).
// A run that discovers mid-work that its scope needs a path no agent can
// land a change to files the author-only half as its own ticket in
// Triage and links it as a blocker of the ticket that found it.
//
// Split rather than labelled in place, and the difference matters. The
// label makes the pipeline route around a ticket in every state, so
// labelling the original would retire work the pipeline is otherwise
// able to do — and hand the author a ticket they have to remember to
// un-label before the rest of it can move. The blocking relation says
// the same thing without that cost: the original stays the pipeline's,
// parked in Blocked, and the queue already refuses to start a ticket
// with an open blocker (DESIGN §9).
//
// Returns the filed ticket's key, or the existing one when an open
// author-only blocker is already in front of this ticket — there is one
// obstacle however many times a run walks into it.
func (p *Plane) FileAuthorOnlyBlocker(ctx context.Context, blockedID, blockedKey, title, description string) (string, error) {
	if err := p.resolveStates(ctx); err != nil {
		return "", err
	}
	// Dedupe off the relation rather than off a marker key, because the
	// relation is the thing that has to be true. A marker in a Triage
	// description only reads back when the team has a Triage state at
	// all — Triage is Linear-managed and setup cannot provision it — so
	// a project without one would file a fresh blocker every run.
	issues, err := p.Tracker.ListIssues(ctx, p.Config.Tracker.TeamID, p.Config.Tracker.ProjectID)
	if err != nil {
		return "", err
	}
	byID := map[string]tracker.Issue{}
	for _, i := range issues {
		byID[i.ID] = i
	}
	for _, id := range byID[blockedID].BlockedBy {
		other, ok := byID[id]
		if !ok || !hasLabel(other.Labels, core.LabelAuthorOnly) {
			continue
		}
		// A closed one is history: the author made that change and the
		// run has walked into a different author-only path, or the same
		// one again for a new reason. Either way it needs its own ticket.
		if st, ok := p.stateByID[other.StateID]; ok && (st == protocol.Done || st == protocol.Canceled) {
			continue
		}
		return other.Key, nil
	}
	stateID, err := p.triageStateID(ctx)
	if err != nil {
		return "", err
	}
	issue, err := p.Tracker.CreateIssue(ctx, tracker.NewIssue{
		TeamID:      p.Config.Tracker.TeamID,
		ProjectID:   p.Config.Tracker.ProjectID,
		Title:       title,
		Description: description,
		StateID:     stateID,
	})
	if err != nil {
		return "", err
	}
	// Label after the create, then link: a create carrying a label the
	// team does not have fails outright, while AddTicketLabel provisions
	// it on the way through. Neither failure is worth discarding the
	// filed ticket over — an under-labelled or unlinked blocker is
	// recoverable by hand, a lost one is not — so both are reported
	// with the key so the caller can say what landed.
	if err := p.AddTicketLabel(ctx, issue.ID, core.LabelAuthorOnly); err != nil {
		return issue.Key, fmt.Errorf("filed %s but could not mark it %s: %w", issue.Key, core.LabelAuthorOnly, err)
	}
	if err := p.Tracker.LinkBlocking(ctx, issue.ID, blockedID); err != nil {
		return issue.Key, fmt.Errorf("filed %s but could not link it as a blocker of %s: %w", issue.Key, blockedKey, err)
	}
	return issue.Key, nil
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
