// Package core is the pure heart of the control plane: a function from a
// snapshot of the world to a list of actions. No I/O, no clock reads, no
// adapter imports — the sweep is decidable from its arguments alone, which
// is what makes the protocol testable exhaustively in Ring 1 and Ring 2
// (PLAN §1) before any credential exists.
package core

import (
	"time"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Role classifies who performed a transition or wrote a comment. The
// adapter resolves tracker identities to roles; the core never sees a user
// id, so identity mapping stays a config concern.
type Role string

const (
	RoleAuthor       Role = "author"
	RoleDesign       Role = "design"
	RoleDev          Role = "dev"
	RoleReconcile    Role = "reconcile"
	RoleBoundary     Role = "boundary"
	RoleControlPlane Role = "controlplane"
	RoleCI           Role = "ci"
	RoleOther        Role = "other"
)

// AgentKind names a dispatchable agent.
type AgentKind string

const (
	AgentDesign    AgentKind = "design"
	AgentDev       AgentKind = "dev"
	AgentReconcile AgentKind = "reconcile"
	AgentBoundary  AgentKind = "boundary"
	// AgentLiveSuite is not an LLM agent: it is the project's live-suite
	// workflow (real network, real providers), dispatched once per
	// milestone against the boundary ticket (DESIGN §10). It rides the
	// same dispatch/run-name machinery as the agents.
	AgentLiveSuite AgentKind = "live-suite"
)

// CIStatus is the state of a ticket's checks, as the adapter reports it.
type CIStatus string

const (
	CINone    CIStatus = ""
	CIPending CIStatus = "pending"
	CIGreen   CIStatus = "green"
	CIRed     CIStatus = "red"
)

// DeployStatus is the adapter's judgment of a Merged ticket's deployment:
// it owns the ancestry comparison and the surface check (DESIGN §11, §13),
// so the core only reads the verdict.
type DeployStatus string

const (
	DeployNone     DeployStatus = ""
	DeployPending  DeployStatus = "pending"
	DeployDeployed DeployStatus = "deployed"
	DeployFailed   DeployStatus = "failed"
)

// Transition is a ticket's most recent state change. The core only ever
// needs the last one: invariants judge how a ticket arrived where it is,
// and once the sweep reverts a transition the revert becomes the last.
type Transition struct {
	From  protocol.State
	To    protocol.State
	Actor Role
	At    time.Time
}

// Run is the most recent agent run dispatched for a ticket.
type Run struct {
	ID      string
	Kind    AgentKind
	Live    bool
	EndedAt time.Time
}

// Comment is one tracker comment, marker or prose.
type Comment struct {
	Body  string
	Actor Role
	At    time.Time
}

// CIInfo is the current check state for the ticket's branch. RunURL
// identifies the specific CI run so the sweep can tell a new failure from
// one it already recorded (DESIGN §12).
type CIInfo struct {
	Status CIStatus
	RunURL string
}

// Ticket is one issue as the sweep sees it.
type Ticket struct {
	ID    string
	Key   string
	Title string
	// Description is the immutable original argument (DESIGN §2.3). The
	// sweep never reads it; the agent harness serves it as scope.
	Description string
	State       protocol.State
	// StateSince is when the ticket entered its current state; grace
	// periods and timeouts measure from here.
	StateSince time.Time
	Last       *Transition
	Labels     []string
	// Priority uses the tracker's scale; 1 is Urgent (DESIGN §8).
	Priority  int
	Milestone string
	// Blocks and BlockedBy are ticket IDs. Blocks drives the boundary
	// drain rule; BlockedBy is verified at dispatch (DESIGN §9, §10).
	Blocks    []string
	BlockedBy []string
	Comments  []Comment
	// AssigneeID is the tracker's current assignee ("" = nobody). The
	// sweep drives it from state (DESIGN §3), never reads it as intent.
	AssigneeID string
	CI         CIInfo
	Deploy     DeployStatus
	Run        *Run
	CreatedAt  time.Time
}

// Snapshot is everything one sweep may consider. Durations that would
// otherwise be config reads are copied in so the core imports no config.
type Snapshot struct {
	Now              time.Time
	CurrentMilestone string
	// AuthorID is the author's tracker id, for assignment (DESIGN §3).
	// Empty turns assignment off rather than assigning nobody, so a
	// project without the mapping keeps whatever a human set.
	AuthorID        string
	KillSwitch      bool
	StaleClaimGrace time.Duration
	DeployTimeout   time.Duration
	Tickets         []*Ticket
}

const urgentPriority = 1

func (t *Ticket) Urgent() bool { return t.Priority == urgentPriority }

func (t *Ticket) HasLabel(name string) bool {
	for _, l := range t.Labels {
		if l == name {
			return true
		}
	}
	return false
}

// IsBoundary reports whether this is a milestone boundary ticket, which is
// special-cased in eight places (DESIGN §10).
func (t *Ticket) IsBoundary() bool { return t.HasLabel(LabelBoundary) }

// MutexLabels returns the ticket's mutex labels — screen: and system:
// alike, one rule for both kinds (DESIGN §6).
func (t *Ticket) MutexLabels() []string {
	var out []string
	for _, l := range t.Labels {
		if hasPrefix(l, protocol.ScreenLabelPrefix) || hasPrefix(l, protocol.SystemLabelPrefix) {
			out = append(out, l)
		}
	}
	return out
}

func hasPrefix(s, prefix string) bool {
	return len(s) > len(prefix) && s[:len(prefix)] == prefix
}

// Resolved reports a ticket that no longer holds or awaits work.
func (t *Ticket) Resolved() bool {
	return t.State == protocol.Done || t.State == protocol.Canceled
}

// InFlight is the DESIGN vocabulary: any state from Ready for dev through
// Merged inclusive. The screen mutex quantifies over this set.
func (t *Ticket) InFlight() bool {
	switch t.State {
	case protocol.ReadyForDev, protocol.InProgress, protocol.Checks, protocol.Reconciling,
		protocol.ReadyForRework, protocol.Reworking, protocol.Merged:
		return true
	}
	return false
}

// LiveRun reports whether the ticket has a live agent run of the given
// kind ("" for any kind).
func (t *Ticket) LiveRun(kind AgentKind) bool {
	return t.Run != nil && t.Run.Live && (kind == "" || t.Run.Kind == kind)
}

// Label names the sweep reads. Defined here rather than in protocol to
// keep protocol's list purely "what setup provisions"; these are the same
// strings.
const (
	LabelReEvaluate  = "re-evaluate"
	LabelNeedsReview = "needs-review"
	LabelBoundary    = "milestone-boundary"
)

func (s *Snapshot) ticket(id string) *Ticket {
	for _, t := range s.Tickets {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// boundaryTicket returns the boundary ticket for the current milestone, if
// one exists in any state.
func (s *Snapshot) boundaryTicket() *Ticket {
	for _, t := range s.Tickets {
		if t.IsBoundary() && t.Milestone == s.CurrentMilestone {
			return t
		}
	}
	return nil
}

// Paused reports whether the queue is paused: a boundary ticket for the
// current milestone is open (DESIGN §10).
func (s *Snapshot) Paused() bool {
	b := s.boundaryTicket()
	return b != nil && !b.Resolved()
}

// agentBusy reports whether any ticket holds a live run of the given kind —
// each agent is singular (DESIGN §5), so one live run means busy.
func (s *Snapshot) agentBusy(kind AgentKind) bool {
	for _, t := range s.Tickets {
		if t.LiveRun(kind) {
			return true
		}
	}
	return false
}
