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
	// FailedJobs names what is red, so the failure comment can say so.
	// The comment is the rework scope (DESIGN §2.3), and a scope that
	// says only "fix what the linked run reports" is a scope for whoever
	// can open the run — which the dev agent, holding no GitHub
	// credential by design, cannot.
	FailedJobs []string
	// Mergeable is whether the branch can still land on main, which is a
	// fact about the ticket's future that no CI verdict carries.
	//
	// It is here rather than discovered at merge time because a
	// conflicted PR never reaches merge time: GitHub cannot build the
	// merge ref, so the `pull_request` run never happens, so no verdict
	// ever arrives, so the ticket sits in Checks — a state with no agent
	// and therefore no stale-claim timeout — indefinitely. The bounce
	// that already existed (reconcile's, on ErrNotMergeable) is
	// downstream of the verdict that will not come.
	//
	// Empty means "GitHub has not said", which is not a conflict.
	Mergeable MergeState
	// PRNumber names the PR in the conflict marker, so the comment reads
	// the same whichever half of the pipeline noticed.
	PRNumber int
}

// MergeState mirrors the host port's tri-state (host.MergeState). The
// core imports no adapter, and the third value is load-bearing: GitHub
// answers "not computed yet" and "conflicted" differently, and only one
// of them is a reason to move a ticket.
type MergeState string

const (
	MergeUnknown    MergeState = ""
	MergeClean      MergeState = "clean"
	MergeConflicted MergeState = "conflicted"
)

// Ticket is one issue as the sweep sees it.
type Ticket struct {
	ID    string
	Key   string
	Title string
	// Description is the immutable original argument (DESIGN §2.3). The
	// sweep never reads it; the agent harness serves it as scope.
	Description string
	// URL is the tracker's link to this ticket, for output a human reads.
	URL   string
	State protocol.State
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
	AuthorID string
	// Recorded is what the pipeline last did to each ticket, keyed by
	// ticket id, from the state store it owns.
	//
	// It exists because the tracker cannot answer "who moved this". The
	// harness authenticates to Linear as the author on a solo workspace,
	// so every pipeline write arrives wearing the author's identity, and
	// a role read off that identity said "control plane" for the human's
	// moves too — which is the one role the revert rules trust, so every
	// §9 invariant was silently off.
	//
	// A ticket absent from this map is not judged. Every ticket that
	// existed before the store did is absent, and treating "I have no
	// record" as "a human did it" would revert the whole backlog on the
	// first sweep.
	Recorded map[string]RecordedMove

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

// RecordedMove is one transition the pipeline made, as the pipeline
// recorded it before making it. Write-ahead, deliberately: a record
// describing a transition that then failed is harmless — the tracker
// still shows the old state, so nothing matches it — while a transition
// that landed without its record reads as a human's and gets reverted,
// then re-made, then reverted again.
type RecordedMove struct {
	From protocol.State
	To   protocol.State
	// Role is which part of the pipeline made the move. The writer
	// matrix judges roles, and the agents' moves have to be judged: a
	// design pass promoting straight past Design review is exactly the
	// thing §9 exists to catch.
	Role Role
}

// HoldsMutex reports whether this ticket's screen and system labels are
// claimed against other tickets (DESIGN §6).
//
// In flight, minus Merged. The mutex exists so two dev agents do not edit
// one screen or one system at the same time — and a merged ticket's work
// is on main, its branch gone, with nothing being written. A ticket
// branching off main afterwards cannot conflict with it; it *contains*
// it.
//
// Counting Merged held the labels for the whole deploy-detection window
// instead, which on a platform with no deploy webhook is up to an hour of
// a queue held by a ticket that is finished. If the deploy fails and the
// author sends it back for rework it re-enters the queue and re-takes the
// mutex then, which is the ordinary contention case rather than a special
// one.
func (t *Ticket) HoldsMutex() bool {
	return t.InFlight() && t.State != protocol.Merged
}

// MutexHolder returns another ticket holding a mutex label this one
// carries, and the label, or nil.
//
// One function, three callers — the promotion revert (DESIGN §6), the
// pickup assertion, and the dispatcher — because they are the same
// question and they must not answer it differently. They did: the
// dispatcher never asked at all, so the sweep dispatched a ticket whose
// label was held straight into a pickup assertion that refused it, every
// beat, at a full billed job each time.
func MutexHolder(s *Snapshot, t *Ticket) (*Ticket, string) {
	for _, other := range s.Tickets {
		if other.ID == t.ID || !other.HoldsMutex() {
			continue
		}
		for _, mine := range t.MutexLabels() {
			if other.HasLabel(mine) {
				return other, mine
			}
		}
	}
	return nil, ""
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
	// LabelNeedsSetup is Blocked's third flavor (DESIGN §12): nothing
	// failed and no judgment is owed — a human has to do something the
	// automation cannot, like putting a secret in an environment. It
	// reads as a failure in a Blocked column otherwise, which is how the
	// Blocked count stops being a health signal.
	LabelNeedsSetup = "needs-setup"
	// LabelNoChanges is Blocked's fourth flavor (DESIGN §12): the run
	// finished and produced nothing, because there was nothing to
	// produce. A ticket reaches an agent because something is meant to
	// change, so a run with no diff is exceptional and almost always
	// means the work reached main by another route and the ticket
	// duplicates it. Parked for the author rather than routed
	// automatically: whether it is a duplicate to cancel or a scope that
	// needs rewriting is a judgment, and the pipeline has no way to make
	// it that is not a guess.
	LabelNoChanges = "no-changes"
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
