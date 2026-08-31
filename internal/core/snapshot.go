// Package core is the pure heart of the control plane: a function from a
// snapshot of the world to a list of actions. No I/O, no clock reads, no
// adapter imports — the sweep is decidable from its arguments alone, which
// is what makes the protocol testable exhaustively in Ring 1 and Ring 2
// (PLAN §1) before any credential exists.
package core

import (
	"time"

	"github.com/SwaggerAllen/orchestration/internal/marker"
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

// RunOutcome is how an agent run ended, as the adapter reports it. The
// adapter owns the mapping from its host's vocabulary, the same way it
// does for CIStatus.
type RunOutcome string

const (
	// OutcomeUnknown is a run still live, one whose host said nothing,
	// or one whose conclusion the adapter has not measured. It is the
	// value that changes no behaviour, and every rule here must leave it
	// alone.
	OutcomeUnknown   RunOutcome = ""
	OutcomeSucceeded RunOutcome = "succeeded"
	OutcomeFailed    RunOutcome = "failed"
	OutcomeCancelled RunOutcome = "cancelled"
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
	// Outcome is how the run ended. Absent it, the stale-claim rule had
	// only "not live" to reason from and had to wait out a grace period
	// to guess at the difference between a run that died and one that is
	// slow to report finishing (DESIGN §12).
	Outcome RunOutcome
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
	// RunAttempt is which attempt of that run produced this verdict. A
	// re-run keeps the id and the URL and increments only this, so the
	// URL alone cannot tell "the failure we already recorded" from "the
	// same run, run again, still failing" — and reading it as the
	// former would swallow a real failure in silence.
	RunAttempt int
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
	// LiveRuns is every run still executing against this ticket. Run
	// collapses to one and that is right for every rule but one — see
	// OtherLiveRun, and the two boundary agents that ran one ticket to
	// completion because nothing could see them both.
	LiveRuns  []Run
	CreatedAt time.Time
	// HandMerges are merges found on the host that this ticket carries no
	// `merged` marker for. Filled by the snapshot build only where the
	// answer is wanted, and read by exactly one rule, which writes the
	// missing marker (DESIGN §11).
	HandMerges []HandMerge
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
	// Triage holds the issues in a triage-category state: filed
	// proposals the author has not accepted or declined yet. They carry
	// no protocol state, which is exactly why they are here and not in
	// Tickets — every rule in this package quantifies over Tickets, and
	// a proposal that appeared there would be dispatchable work nobody
	// had agreed to (DESIGN §10).
	//
	// Kept rather than dropped because one pass does need them. The
	// boundary's composition proposal names what the next debt milestone
	// should hold, and it runs seconds after the file step created these
	// — so a composition reading only Tickets reported "nothing to
	// schedule" immediately after filing eight proposals. Catapult's
	// ORC-45, second pass.
	Triage []*Ticket
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

// Unmanaged reports a ticket the pipeline does not drive: it neither
// dispatches against it, promotes it, corrects its state, nor judges how
// it arrived where it is.
//
// One predicate over two labels because the question every caller asks
// is the same one, and a rule stated in more than one place is a rule
// that gets amended in one of them. The reasons differ — author-only is
// work the author owns end to end (DESIGN §8), resync is a record being
// repaired (DESIGN §9) — and the answer to "does the pipeline drive
// this" does not.
func (t *Ticket) Unmanaged() bool {
	return t.HasLabel(LabelAuthorOnly) || t.HasLabel(LabelResync)
}

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
// Author-only tickets never hold it: they are the author's from Todo to
// Done and no agent will ever be dispatched against one (DESIGN §8), so
// counting one would park every ticket sharing its screen or system
// behind work the pipeline is not doing and cannot observe finishing.
func (t *Ticket) HoldsMutex() bool {
	// Author-only, not Unmanaged: a resync ticket keeps its mutex. The
	// label repairs where the pipeline thinks the ticket is, and says
	// nothing about the branch it may still have open on that system —
	// releasing the label here would let a second ticket start on the
	// same files while the first one's work is still out there. An
	// author-only ticket has no agent coming for it at all, which is a
	// different fact and the one that exclusion rests on.
	if t.HasLabel(LabelAuthorOnly) {
		return false
	}
	return t.InFlight() && t.State != protocol.Merged
}

// MutexHolder returns another ticket holding a mutex label this one
// carries, and the label, or nil. This is the strict reading of DESIGN
// §6's invariant — every in-flight ticket short of Merged holds — and it
// is what the promotion door asks: may this ticket *enter* the queue.
//
// MutexBlocker is what the dispatcher and the pickup assertion ask: may
// work *start* on it now. The two questions differ by the tie-break
// below, and nothing else: they share this loop, so the set of holders
// and the label reported can never drift apart.
//
// Those two in particular must agree, and once did not — the dispatcher
// never asked at all, so the sweep dispatched a ticket whose label was
// held straight into a pickup assertion that could only refuse, every
// beat, at a full billed job each time. That is why they call one
// function rather than two, and why the tie-break lives here rather than
// at either call site.
func MutexHolder(s *Snapshot, t *Ticket) (*Ticket, string) {
	return mutexHolder(s, t, false)
}

// MutexBlocker is MutexHolder with the deadlock tie-break applied: an
// idle queued holder yields to a ticket that precedes it.
//
// Without it a pair of tickets sharing a label and both sitting in a
// queue are each other's holder, so neither dispatches and neither can
// ever be picked up — the mutex stops serializing them and starts
// stalling both. The invariant is enforced at exactly one door, the
// promotion revert, and nothing guards the others: a CI-red bounce lands
// a ticket in Ready for rework whether or not another already holds its
// label, and the author moving one out of Blocked does the same.
//
// Measured on Catapult, not reproduced from the rule: ORC-171 and
// ORC-174 share system:delivery and system:platform_content, and both sat
// in Ready for rework from 2026-08-31T03:37:28Z to 05:12:52Z — an hour
// and thirty-five minutes with the dev agent idle and the sweep planning
// nothing. It broke when the author moved ORC-171 to Blocked at 05:12:52,
// taking it out of the in-flight set; ORC-174 started Reworking 77
// seconds later, on the next sweep.
//
// The tie-break only ever unblocks, and it serializes rather than
// widening: precedence is a total order (Precedes falls through to the
// key), so of any set of idle queued tickets sharing a label exactly one
// is free and the rest are held by it. The winner's claim writes its
// working state, which is not idle, so from the next beat it holds the
// label against the others unconditionally. Two agents are never in one
// system at one time, which is what DESIGN §6 is for; what changes is
// that "both waiting" no longer reads as "both working".
//
// The promotion door keeps the strict reading deliberately. It is the
// preventive half of §6 and the deadlock does not arrive through it —
// both tickets above reached Ready for rework by bouncing, a path that
// door never sees — so relaxing it would widen what may queue on one
// system while rescuing nothing.
func MutexBlocker(s *Snapshot, t *Ticket) (*Ticket, string) {
	return mutexHolder(s, t, true)
}

func mutexHolder(s *Snapshot, t *Ticket, yieldIdle bool) (*Ticket, string) {
	for _, other := range s.Tickets {
		if other.ID == t.ID || !other.HoldsMutex() {
			continue
		}
		// The tie-break. Tested on the holder alone, never on t: at
		// pickup the claiming run is already live on t, so a condition
		// reading t.queuedIdle() would be true at dispatch and false at
		// pickup — the two answering differently again, through a door
		// built to stop exactly that.
		if yieldIdle && other.queuedIdle() && Precedes(t, other) {
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

// queuedIdle reports a ticket waiting in a dev queue with no agent on it:
// in flight for the mutex, but with nothing running that could be editing
// the files the label covers.
func (t *Ticket) queuedIdle() bool {
	return (t.State == protocol.ReadyForDev || t.State == protocol.ReadyForRework) && !t.LiveRun("")
}

// HandMerge is a merge the pipeline did not make: a PR the author
// merged, found by looking the ticket's branch up on the host.
type HandMerge struct {
	SHA string
	PR  string
}

// MergedSHAs returns the ticket's merge commits from its `merged`
// markers, oldest first — the order the comments are in, which is the
// order the commits landed.
//
// All of them, not just the newest: a ticket merged, reverted by hand
// and merged again has two, and a caller carrying one would leave half
// of it on main when the rehearsal reset ran.
//
// One reader, because there were three near-copies of this loop and
// they did not agree — the retro note took every marker and the deploy
// check took the last, which is a real difference worth keeping, but it
// is now the difference between this function and its caller rather
// than between two hand-written loops that could drift apart.
func MergedSHAs(t *Ticket) []string {
	var out []string
	for _, c := range t.Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok || m.Kind != marker.Merged {
			continue
		}
		if sha := m.Fields["sha"]; sha != "" {
			out = append(out, sha)
		}
	}
	return out
}

// LiveRun reports whether the ticket has a live agent run of the given
// kind ("" for any kind).
func (t *Ticket) LiveRun(kind AgentKind) bool {
	return t.Run != nil && t.Run.Live && (kind == "" || t.Run.Kind == kind)
}

// OtherLiveRun returns a run of this kind executing against this ticket
// that is not the caller's, or nil.
//
// Run collapses to one run per ticket — the live one, or the most
// recently ended — which is what every other rule wants and is exactly
// wrong for this question. Two boundary agents ran ORC-45 to completion
// concurrently and nothing refused the second: the pickup assertion asks
// whether another *ticket* holds a live run of the kind, and both runs
// were on the same ticket, so the ticket's own run was skipped as "mine"
// by both of them. Whichever run Run happened to collapse to, the other
// one recognised it as itself.
func (t *Ticket) OtherLiveRun(kind AgentKind, runID string) *Run {
	if runID == "" {
		// No id to compare against, so every run looks like somebody
		// else's. Refusing on that would break a claim whose harness
		// could not tell it its own run id, which is worse than the race.
		return nil
	}
	for i, r := range t.LiveRuns {
		if r.ID != runID && (kind == "" || r.Kind == kind) {
			return &t.LiveRuns[i]
		}
	}
	return nil
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
	// A run that changes no files is exceptional — work reaches an agent
	// because something is meant to change — and there are three
	// different reasons for it, wanting three different things from a
	// human. They get three labels rather than one, because a Blocked
	// column that cannot tell them apart is one somebody has to open
	// every ticket to read.
	//
	// LabelScopeSatisfied: everything the ticket asks for is already on
	// main. Almost always a duplicate of merged work, which is Canceled
	// rather than Done (DESIGN §2.6) — but sometimes a scope that went
	// stale and wants rewriting, and only a human can tell which.
	LabelScopeSatisfied = "scope-satisfied"
	// LabelPushback: the design cannot be built as drawn (DESIGN §2.7).
	// Parks rather than routing back to Designing, because a design pass
	// runs on every entry to Designing and nothing counts the trips — a
	// decisionless pass and a push-back can hand the same ticket back and
	// forth indefinitely, each one correct on its own terms. Blocked puts
	// a human in the loop, which is the only thing here that can break it.
	LabelPushback = "pushback"
	// LabelAuthorOnly routes a ticket to the author instead of an agent:
	// no design pass is dispatched for it and the dev queue skips it, in
	// every state, so it moves only when a human moves it.
	//
	// It exists because some work is legal for nobody else. The quality
	// gates live in pipeline.config.json and ci.yml; both are author-only
	// (DESIGN §5), so a ticket scoped to change the gate set had no
	// agent-legal path to completion — the dev agent would claim it, find
	// every file it needed closed to it, and hand back. That is a full
	// run spent to be told no, repeated on every beat, because the
	// refusal leaves the ticket in the queue.
	//
	// A label rather than a state: it says who owns the work, not where
	// the work is, and the ticket still moves through the ordinary states
	// as the author does it.
	LabelAuthorOnly = "author-only"
	// LabelResync hands one ticket back to the author to repair its
	// state by hand, and is the only way out of a state mix-up the
	// rules cannot untangle themselves.
	//
	// Functionally close to author-only and semantically not: author-only
	// says "this work is the author's, end to end" and is a property of
	// the ticket; resync says "the pipeline's idea of where this ticket
	// is has come apart from the tracker's, and I am fixing it" — a
	// temporary state of the *record*, removed when the repair is done.
	//
	// The half that makes it work is not the hands-off part. The
	// pipeline records only its own writes (DESIGN §9), so an author
	// move it merely permits leaves the record where the pipeline last
	// wrote — and the next move is then judged from that stale origin.
	// Author-only alone is no help here, and was tried: it suppresses
	// the judgement while it is on, the record still does not move, and
	// taking it off re-exposes the identical divergence to the identical
	// revert. So a resync ticket has its current state adopted into the
	// record on every sweep, which is what leaves the pipeline agreeing
	// with the tracker at the moment the label comes off.
	LabelResync = "resync"
	// LabelPrerequisite: the ticket's scope depends on something that is
	// not on main and is not this ticket's to write, so there is nothing
	// the pass can decide yet.
	//
	// Its own flavor rather than needs-setup, which it otherwise fits
	// ("a human has to do something the run cannot"), because the human
	// action is different and so is the clearing condition: needs-setup
	// wants a secret provisioned, this wants another change merged and
	// then this ticket put back in its queue. A Blocked column that
	// renders the two identically is one where the author has to open
	// each ticket to find out whether anything is owed of them yet.
	//
	// Before it existed a design pass in this position had no legal
	// outcome at all — `artifacts` claims there is something to approve
	// and `decisionless` claims the scope was examined and needs no
	// decision — so the run died and the ticket landed in Blocked under
	// `failed`, reading as a harness fault. Catapult's ORC-157 is the
	// measurement: it happened twice on one ticket, and the follow-up
	// filed to carry the second occurrence was cancelled.
	LabelPrerequisite = "prerequisite"
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
