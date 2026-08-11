package core

import (
	"fmt"
	"sort"

	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Sweep is the control plane: one pass over the snapshot producing the
// actions the trigger table, the invariants and the escalation rules call
// for (DESIGN §9, §12, §13). It is convergent rather than one-shot
// idempotent: an action can enable the next (a ticket bounced to the queue
// is then dispatched), and successive sweeps — cron ticks in production,
// the convergence loop in the sim — reach a fixpoint. Ring 2 asserts the
// fixpoint on every scenario.
//
// Rule order matters and is deliberate: corrections and reverts first, so
// no later rule dispatches work against a state the sweep itself is about
// to undo.
func Sweep(s *Snapshot) []Action {
	// Kill switch: nothing new starts, in-flight runs finish (DESIGN §13).
	// It halts corrections too — the author flipping it wants the system's
	// hands off the tracker, and a revert is a write like any other.
	if s.KillSwitch {
		return nil
	}

	var acts []Action
	tickets := make([]*Ticket, len(s.Tickets))
	copy(tickets, s.Tickets)
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].Key < tickets[j].Key })

	// reverted tracks tickets this sweep is already correcting, so later
	// rules don't act on a state that is about to be undone.
	reverted := map[string]bool{}

	for _, t := range tickets {
		if a, ok := revertFor(s, t); ok {
			acts = append(acts, a...)
			reverted[t.ID] = true
		}
	}
	for _, t := range tickets {
		if reverted[t.ID] {
			continue
		}
		acts = append(acts, correctionsFor(s, t)...)
	}
	for _, t := range tickets {
		if reverted[t.ID] {
			continue
		}
		acts = append(acts, ciFor(s, t)...)
	}
	// Assignment last among the per-ticket rules: it is derived from the
	// state a sweep leaves behind, and a ticket this pass is moving gets
	// its assignee on the next one rather than one keystroke early.
	for _, t := range tickets {
		if reverted[t.ID] {
			continue
		}
		acts = append(acts, assignmentFor(s, t)...)
	}
	for _, t := range tickets {
		if reverted[t.ID] {
			continue
		}
		acts = append(acts, escalationsFor(s, t)...)
		acts = append(acts, postDeployFor(s, t)...)
		acts = append(acts, staleClaimFor(s, t)...)
	}

	acts = append(acts, boundaryFor(s)...)

	// A ticket this sweep is already moving must not also be dispatched —
	// the dispatch would race the transition it hasn't seen. The next
	// sweep dispatches from the settled state.
	moving := map[string]bool{}
	for _, a := range acts {
		if a.Kind == ActTransition {
			moving[a.TicketID] = true
		}
	}
	for id := range reverted {
		moving[id] = true
	}
	acts = append(acts, dispatches(s, moving)...)
	return acts
}

// revertFor enforces the §9 invariants on how the ticket arrived in its
// current state: detect-and-revert, because the tracker has no
// pre-transition hook. Only the last transition is judged — once reverted,
// the revert becomes the last transition and the rule cannot re-fire.
func revertFor(s *Snapshot, t *Ticket) ([]Action, bool) {
	last := t.Last
	switch {
	case last == nil, last.To != t.State:
		// No arrival to judge, or the snapshot is mid-change; the next
		// sweep sees a consistent picture.
		return nil, false
	case last.Actor == RoleControlPlane:
		// The sweep trusts its own writes, or reverts would oscillate.
		return nil, false
	case t.IsBoundary():
		// The boundary ticket has its own state meanings and its own
		// owner; the matrix below would misjudge it (DESIGN §10).
		return nil, false
	case last.From == protocol.Blocked && last.Actor == RoleAuthor:
		// Only the author moves a ticket out of Blocked, and they choose
		// the state — any state (DESIGN §12).
		return nil, false
	}

	revert := func(rule, prose string) []Action {
		return []Action{{
			Kind:     ActTransition,
			TicketID: t.ID,
			To:       last.From,
			Marker: &marker.Marker{Kind: marker.Revert, Fields: map[string]string{
				"rule": rule,
				"from": string(last.From),
				"to":   string(last.To),
			}},
			Prose:  prose,
			Reason: "invariant: " + rule,
		}}
	}

	// No forward transition while re-evaluate is set (DESIGN §7, §9) —
	// except into Designing, which is where the flag gets folded into the
	// live pass rather than fought (DESIGN §7's Backlog/Todo row).
	if t.HasLabel(LabelReEvaluate) && forward(last.From, last.To) && last.To != protocol.Designing {
		return revert("re-evaluate",
			"This ticket carries re-evaluate: an unresolved collision. It cannot move forward until the owning thread clears the label."), true
	}

	// The mutex — screen and system labels under one rule — enforced at
	// promotion into Ready for dev (DESIGN §6).
	if t.State == protocol.ReadyForDev {
		for _, other := range s.Tickets {
			if other.ID == t.ID || !other.InFlight() {
				continue
			}
			for _, mine := range t.MutexLabels() {
				if other.HasLabel(mine) {
					return revert("mutex",
						fmt.Sprintf("Mutex label %q is already in flight on %s. Two in-flight tickets may not share a screen or a system (DESIGN §6).", mine, other.Key)), true
				}
			}
		}
	}

	// Writer matrix: who may write which state (DESIGN §3, §9).
	if rule, prose, bad := writerViolation(t, last); bad {
		return revert(rule, prose), true
	}
	return nil, false
}

// writerViolation applies the "written by" column of the state table. It
// judges roles, not identities — the adapter resolves who is who.
func writerViolation(t *Ticket, last *Transition) (rule, prose string, bad bool) {
	deny := func(r, p string) (string, string, bool) { return r, p, true }
	switch last.To {
	case protocol.ReadyForDev:
		// Sign-off is the author's, from Design review, always — except a
		// decisionless design pass, declared by marker, which advances
		// straight from Designing (DESIGN §3, §6).
		switch {
		case last.Actor == RoleAuthor && last.From == protocol.DesignReview:
		case last.Actor == RoleDesign && last.From == protocol.Designing && hasDecisionlessPass(t):
		default:
			return deny("sign-off",
				"Ready for dev is entered by the author's sign-off from Design review, or by a design pass that declared itself decisionless. Neither happened here.")
		}
	case protocol.InProgress, protocol.Reworking:
		if last.Actor != RoleDev {
			return deny("claim",
				"In progress and Reworking are written by the dev agent as its claim. A hand-moved ticket has no run behind it.")
		}
	case protocol.Checks:
		if last.Actor != RoleDev {
			return deny("checks-writer", "Checks is written by the dev agent when it flips the draft off.")
		}
	case protocol.Reconciling:
		if last.Actor != RoleCI {
			return deny("reconciling-writer", "Reconciling is written by the control plane on CI green.")
		}
	case protocol.Merged:
		if last.Actor != RoleReconcile {
			return deny("merge-writer", "Only reconciliation merges (DESIGN §9).")
		}
	case protocol.Done:
		// Only the post-deploy check writes Done; the boundary ticket is
		// excepted and already excluded above (DESIGN §9, §10).
		return deny("done-writer", "Only the post-deploy check writes Done (DESIGN §9).")
	case protocol.BoundaryReview:
		return deny("boundary-state", "Boundary review is used only by the milestone boundary ticket (DESIGN §3).")
	case protocol.DesignReview:
		if last.Actor != RoleDesign {
			return deny("design-review-writer", "Design review is written by the design agent when artifacts are ready.")
		}
	}
	return "", "", false
}

// correctionsFor handles the mechanical, non-revert fixes.
func correctionsFor(s *Snapshot, t *Ticket) []Action {
	var acts []Action

	// A Backlog issue carrying the current milestone is a contradiction;
	// correcting it is mechanical, not a judgment (DESIGN §10).
	if t.State == protocol.Backlog && t.Milestone == s.CurrentMilestone && s.CurrentMilestone != "" && !t.IsBoundary() {
		acts = append(acts, Action{
			Kind: ActTransition, TicketID: t.ID, To: protocol.Todo,
			Reason: "current-milestone tickets are committed, and committed is Todo (DESIGN §10)",
		})
	}

	// re-evaluate is cleared automatically on entry to Designing from
	// Backlog or Todo — nothing was built, the live pass absorbs it
	// (DESIGN §7).
	if t.State == protocol.Designing && t.HasLabel(LabelReEvaluate) && t.Last != nil &&
		(t.Last.From == protocol.Backlog || t.Last.From == protocol.Todo) {
		acts = append(acts, Action{
			Kind: ActRemoveLabel, TicketID: t.ID, Label: LabelReEvaluate,
			Reason: "re-evaluate clears on entry to Designing; the pass folds it in (DESIGN §7)",
		})
	}

	// A flagged ticket in Design review returns to Designing: nothing is
	// built and revision is cheap (DESIGN §7).
	if t.State == protocol.DesignReview && t.HasLabel(LabelReEvaluate) {
		acts = append(acts, Action{
			Kind: ActTransition, TicketID: t.ID, To: protocol.Designing,
			Reason: "re-evaluate on Design review returns to Designing (DESIGN §7)",
		})
	}
	return acts
}

// ciFor turns CI results on a Checks ticket into transitions (DESIGN §12,
// §13). In production the project stub delivers these event-driven; the
// sweep computes the same answers so the polled loop backstops lost events.
func ciFor(s *Snapshot, t *Ticket) []Action {
	if t.State != protocol.Checks || t.IsBoundary() {
		return nil
	}
	switch t.CI.Status {
	case CIGreen:
		// re-evaluate holds promotion: the dev thread evaluates in place
		// and clears or returns to Reworking (DESIGN §7).
		if t.HasLabel(LabelReEvaluate) {
			return nil
		}
		// Reconciling means "the reconcile agent, now" — promotion waits
		// for the agent to be free rather than queueing inside the state.
		if s.agentBusy(AgentReconcile) {
			return nil
		}
		return []Action{
			{Kind: ActTransition, TicketID: t.ID, To: protocol.Reconciling,
				Reason: "CI green on a non-draft PR (DESIGN §13)"},
			{Kind: ActDispatch, TicketID: t.ID, Agent: AgentReconcile,
				Reason: "reconcile the green PR against the argument"},
		}
	case CIRed:
		if t.CI.RunURL == "" || hasMarkerField(t, marker.CIRed, "run", t.CI.RunURL) {
			// Already recorded (or unidentifiable); the earlier sweep
			// moved the ticket. Nothing new to say.
			return nil
		}
		attempt := len(markersOf(t, marker.CIRed)) + 1
		m := &marker.Marker{Kind: marker.CIRed, Fields: map[string]string{
			"run":     t.CI.RunURL,
			"attempt": fmt.Sprintf("%d", attempt),
		}}
		if attempt >= 2 {
			return []Action{{
				Kind: ActTransition, TicketID: t.ID, To: protocol.Blocked, Marker: m,
				Prose:  "Second CI failure on this branch. Two reds is rarely a flake — the author decides whether this is scope, design, or infrastructure (DESIGN §12).",
				Reason: "CI red twice on the same branch",
			}}
		}
		return []Action{{
			Kind: ActTransition, TicketID: t.ID, To: protocol.ReadyForRework, Marker: m,
			Prose:  "CI failed. This comment is the newest, so it is the scope (DESIGN §2.3): fix what the linked run reports.",
			Reason: "CI red, first failure on this branch",
		}}
	}
	return nil
}

// escalationsFor applies the marker-counted escalations that are not CI
// (DESIGN §12).
func escalationsFor(s *Snapshot, t *Ticket) []Action {
	// Second bounce from reconciliation: the queue ticket carries two
	// bounce markers, and two failures to land the same scope is a design
	// problem, not an implementation one.
	if t.State == protocol.ReadyForRework && len(markersOf(t, marker.ReconcileBounce)) >= 2 {
		return []Action{{
			Kind: ActTransition, TicketID: t.ID, To: protocol.Blocked,
			Prose:  "Second bounce from reconciliation on the same ticket. This is a design problem, not an implementation one — Designing is the usual route from here (DESIGN §12).",
			Reason: "second reconcile bounce",
		}}
	}
	return nil
}

// postDeployFor is the thin post-deploy check (DESIGN §11): mechanical, no
// judgment. The adapter owns ancestry and the surface check; the sweep
// routes verdicts.
func postDeployFor(s *Snapshot, t *Ticket) []Action {
	if t.State != protocol.Merged || t.IsBoundary() {
		return nil
	}
	switch t.Deploy {
	case DeployDeployed:
		if t.HasLabel(LabelNeedsReview) {
			return []Action{{
				Kind: ActTransition, TicketID: t.ID, To: protocol.Blocked,
				Prose:  "Deployed clean, but reconciliation could not tell whether this landed as asked (needs-review). A human look closes it (DESIGN §11).",
				Reason: "deployed with needs-review",
			}}
		}
		return []Action{{
			Kind: ActTransition, TicketID: t.ID, To: protocol.Done,
			Reason: "deployed and clean (DESIGN §11)",
		}}
	case DeployFailed:
		return []Action{{
			Kind: ActTransition, TicketID: t.ID, To: protocol.Blocked,
			Prose:  "The deployment carrying this merge failed. Recovery is a redeploy, not a state change, so the author decides where this goes (DESIGN §12).",
			Reason: "deployment failed",
		}}
	default:
		if s.DeployTimeout > 0 && s.Now.Sub(t.StateSince) > s.DeployTimeout {
			return []Action{{
				Kind: ActTransition, TicketID: t.ID, To: protocol.Blocked,
				Prose:  "Merged past the deploy timeout with no deployment covering it — merged but never deployed is otherwise invisible (DESIGN §12).",
				Reason: "deploy timeout",
			}}
		}
	}
	return nil
}

// agentOwner maps each agent-owned state to who should be running it, for
// stale-claim detection (DESIGN §12).
func agentOwner(t *Ticket) (AgentKind, bool) {
	if t.IsBoundary() {
		if t.State == protocol.InProgress {
			return AgentBoundary, true
		}
		return "", false
	}
	switch t.State {
	case protocol.Designing:
		return AgentDesign, true
	case protocol.InProgress, protocol.Reworking:
		return AgentDev, true
	case protocol.Reconciling:
		return AgentReconcile, true
	}
	return "", false
}

// staleClaimFor moves tickets whose claiming run died to Blocked. A ticket
// with no run at all is not stale — it is awaiting dispatch, which the
// dispatch rules handle.
func staleClaimFor(s *Snapshot, t *Ticket) []Action {
	kind, owned := agentOwner(t)
	if !owned || t.Run == nil || t.Run.Live {
		return nil
	}
	// Measure from whichever is later: the run's death or the state entry.
	since := t.Run.EndedAt
	if t.StateSince.After(since) {
		since = t.StateSince
	}
	if s.StaleClaimGrace <= 0 || s.Now.Sub(since) <= s.StaleClaimGrace {
		return nil
	}
	return []Action{{
		Kind: ActTransition, TicketID: t.ID, To: protocol.Blocked,
		Marker: &marker.Marker{Kind: marker.StaleClaim, Fields: map[string]string{
			"state": string(t.State),
			"run":   t.Run.ID,
		}},
		Prose:  fmt.Sprintf("The %s run claiming this ticket is no longer live and the grace period passed. At one agent a stuck claim halts the queue, so it is detected rather than waited out (DESIGN §12).", kind),
		Reason: "stale claim",
	}}
}

// boundaryFor creates the boundary ticket when the last milestone ticket
// resolves (DESIGN §10). The boundary ticket itself is excluded from the
// trigger, or it would retrigger itself forever.
func boundaryFor(s *Snapshot) []Action {
	if s.CurrentMilestone == "" || s.boundaryTicket() != nil {
		return nil
	}
	any := false
	var outstanding []string
	for _, t := range s.Tickets {
		if t.Milestone != s.CurrentMilestone || t.IsBoundary() {
			continue
		}
		any = true
		if !t.Resolved() {
			return nil
		}
		if t.HasLabel(LabelReEvaluate) {
			outstanding = append(outstanding, t.Key+" carries re-evaluate")
		}
	}
	if !any {
		return nil
	}
	prose := ""
	if len(outstanding) > 0 {
		prose = "Outstanding at the gate (DESIGN §10): " + fmt.Sprint(outstanding)
	}
	return []Action{{
		Kind: ActCreateBoundary, Milestone: s.CurrentMilestone, Prose: prose,
		Reason: "last ticket in the milestone resolved; queue pauses (DESIGN §10)",
	}}
}

// dispatches plans agent runs, one per agent kind at most, in precedence
// order (DESIGN §7, §13). moving excludes tickets this sweep already acts on.
func dispatches(s *Snapshot, moving map[string]bool) []Action {
	var acts []Action
	ordered := make([]*Ticket, 0, len(s.Tickets))
	for _, t := range s.Tickets {
		if !moving[t.ID] {
			ordered = append(ordered, t)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return Precedes(ordered[i], ordered[j]) })

	paused := s.Paused()
	boundary := s.boundaryTicket()

	// Design agent: tickets in Designing awaiting dispatch, plus
	// re-evaluate re-reads on queue tickets — the flag blocks pickup and
	// design is the thread that clears it (DESIGN §7).
	if !s.agentBusy(AgentDesign) {
		for _, t := range ordered {
			if t.IsBoundary() || t.LiveRun("") {
				continue
			}
			switch {
			case t.State == protocol.Designing && awaitingDispatch(t):
				// awaitingDispatch keeps this from resurrecting a crashed
				// run — that is the stale-claim rule's call, and the
				// author's after it (DESIGN §12).
				acts = append(acts, Action{Kind: ActDispatch, TicketID: t.ID, Agent: AgentDesign,
					Reason: "ticket in Designing with no live run (DESIGN §13)"})
			case (t.State == protocol.ReadyForDev || t.State == protocol.ReadyForRework) && t.HasLabel(LabelReEvaluate):
				acts = append(acts, Action{Kind: ActDispatch, TicketID: t.ID, Agent: AgentDesign,
					Reason: "re-evaluate blocks pickup; design re-reads (DESIGN §7)"})
			default:
				continue
			}
			break
		}
	}

	// Dev agent: singular, from the queues, respecting the pause, blocking
	// relations, re-evaluate, and the boundary label (DESIGN §5, §7, §10).
	if !devBusy(s) {
		for _, t := range ordered {
			if t.State != protocol.ReadyForDev && t.State != protocol.ReadyForRework {
				continue
			}
			if t.IsBoundary() || t.HasLabel(LabelBoundary) || t.HasLabel(LabelReEvaluate) || t.LiveRun("") {
				continue
			}
			if blockedByOpen(s, t) {
				continue
			}
			// During the pause: only tickets blocking the boundary ticket,
			// plus Urgent, which overrides the pause (DESIGN §8, §10).
			if paused && !t.Urgent() && (boundary == nil || !blocks(t, boundary.ID)) {
				continue
			}
			acts = append(acts, Action{Kind: ActDispatch, TicketID: t.ID, Agent: AgentDev,
				Reason: fmt.Sprintf("head of the queue from %s (DESIGN §7)", t.State)})
			break
		}
	}

	// Boundary agent: the author moving the boundary ticket to In progress
	// is the signal, and re-entry after a failure is the resume path — the
	// agent reads its own step comments and picks up where it stopped.
	// It does not begin while a blocker is open (DESIGN §10).
	if boundary != nil && boundary.State == protocol.InProgress &&
		awaitingDispatch(boundary) && !s.agentBusy(AgentBoundary) && !openBlockerFor(s, boundary.ID) {
		acts = append(acts, Action{Kind: ActDispatch, TicketID: boundary.ID, Agent: AgentBoundary,
			Reason: "author signalled the manual pass is done (DESIGN §10)"})
	}

	// Live suite: dispatched once when the boundary ticket opens, before
	// the author's pass, so the pass reads real end-to-end results
	// (DESIGN §10). The run posts its own result marker; the marker is
	// what ends the loop. A dead run without one is the author's to
	// re-run — the sweep does not resurrect it, for the same reason
	// stale claims are detected rather than silently retried (§12).
	if boundary != nil && boundary.State == protocol.Todo &&
		len(markersOf(boundary, marker.LiveSuite)) == 0 &&
		awaitingDispatch(boundary) && !s.agentBusy(AgentLiveSuite) {
		acts = append(acts, Action{Kind: ActDispatch, TicketID: boundary.ID, Agent: AgentLiveSuite,
			Reason: "boundary open: live suite before the author's pass (DESIGN §10)"})
	}
	return acts
}

// openBlockerFor reports whether any open ticket blocks the given one.
func openBlockerFor(s *Snapshot, id string) bool {
	for _, t := range s.Tickets {
		if !t.Resolved() && blocks(t, id) {
			return true
		}
	}
	return false
}

// awaitingDispatch: no run ever, or the state was re-entered after the last
// run died — the author's recovery path. A run that died while the ticket
// sat in the state is not awaiting dispatch; it is a stale claim, and
// resurrecting it silently would hide the failure the grace period exists
// to surface.
func awaitingDispatch(t *Ticket) bool {
	if t.Run == nil {
		return true
	}
	return !t.Run.Live && t.StateSince.After(t.Run.EndedAt)
}

// devBusy: the dev agent is singular — busy if any ticket holds a live dev
// run, or is sitting in a dev-owned state at all (a dead run there is the
// stale-claim rule's business, not a reason to double-dispatch).
func devBusy(s *Snapshot) bool {
	for _, t := range s.Tickets {
		if t.LiveRun(AgentDev) {
			return true
		}
		if !t.IsBoundary() && (t.State == protocol.InProgress || t.State == protocol.Reworking) {
			return true
		}
	}
	return false
}

func blocks(t *Ticket, id string) bool {
	for _, b := range t.Blocks {
		if b == id {
			return true
		}
	}
	return false
}

// blockedByOpen reports whether any ticket blocking this one is still
// open — verified at pickup (DESIGN §9).
func blockedByOpen(s *Snapshot, t *Ticket) bool {
	for _, id := range t.BlockedBy {
		if other := s.ticket(id); other != nil && !other.Resolved() {
			return true
		}
	}
	return false
}
