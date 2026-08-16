package core

import (
	"fmt"
	"sort"
	"strings"

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
// Rule order matters and is deliberate, in two directions.
//
// Corrections and reverts run first, so no later rule dispatches work
// against a state the sweep itself is about to undo.
//
// Everything else runs **furthest down the pipeline first**, and each
// planned transition is folded into a working copy so the rules that run
// after it see the state it produces. Without that fold a pass reasons
// entirely about the world as it was at the top of the sweep, and every
// hop takes its own beat to become visible: a ticket whose deploy landed
// sat in `Merged` holding its mutex labels for the whole pass, and the
// queued ticket sharing a label was dispatched into a pickup assertion
// that could not pass — once per beat, at a full billed job each time.
// Resolution before consumption is the whole ordering.
//
// It stays a pure function. No I/O, same input to same output; the fold
// happens on a copy, and the caller's snapshot is untouched. What makes
// it safe to act on a prediction is downstream: Execute applies actions
// in slice order and stops at the first error, so if the transition a
// later action assumed never lands, that later action never runs either.
func Sweep(s *Snapshot) []Action {
	// Kill switch: nothing new starts, in-flight runs finish (DESIGN §13).
	// It halts corrections too — the author flipping it wants the system's
	// hands off the tracker, and a revert is a write like any other.
	if s.KillSwitch {
		return nil
	}

	// The working copy. Rules read w; the caller's snapshot is never
	// written, so a caller that sweeps twice gets the same answer twice.
	w := s.working()

	var acts []Action
	tickets := make([]*Ticket, len(w.Tickets))
	copy(tickets, w.Tickets)
	// Deepest first, by key within a rank so the order is total and the
	// output is reproducible.
	sort.Slice(tickets, func(i, j int) bool {
		di, dj := pipelineDepth(tickets[i].State), pipelineDepth(tickets[j].State)
		if di != dj {
			return di > dj
		}
		return tickets[i].Key < tickets[j].Key
	})

	// moved tracks tickets this sweep already has a transition for. One
	// transition per ticket per pass: later rules must not act on a state
	// that is about to be undone, and — now that transitions are folded —
	// must not act on one this pass has just produced either.
	moved := map[string]bool{}
	plan := func(a []Action) {
		if len(a) == 0 {
			return
		}
		acts = append(acts, a...)
		w.fold(a, moved)
	}

	for _, t := range tickets {
		if a, ok := revertFor(w, t); ok {
			plan(a)
		}
	}
	unmoved := func(fn func(*Snapshot, *Ticket) []Action) {
		for _, t := range tickets {
			if moved[t.ID] {
				continue
			}
			plan(fn(w, t))
		}
	}
	unmoved(correctionsFor)
	// Resolution first: a deploy that landed retires the ticket, which
	// releases its mutex labels and clears it as a blocker for everything
	// judged after this point.
	unmoved(postDeployFor)
	unmoved(ciFor)
	unmoved(escalationsFor)
	unmoved(staleClaimFor)

	// The boundary ticket is created when the last milestone ticket
	// resolves, so it runs after the resolutions above rather than
	// against the state they replaced.
	plan(boundaryFor(w))

	// Assignment after every transition is known: it is derived from the
	// state a sweep leaves behind, and a ticket this pass is moving gets
	// its assignee on the next one rather than one keystroke early.
	for _, t := range tickets {
		if moved[t.ID] {
			continue
		}
		acts = append(acts, assignmentFor(w, t)...)
	}

	// A ticket this sweep is already moving must not also be dispatched —
	// the dispatch would race the transition it hasn't seen. The next
	// sweep dispatches from the settled state. Other tickets, though, are
	// dispatched against the folded world, which is the point.
	acts = append(acts, dispatches(w, moved)...)
	return acts
}

// working returns a copy the pass may write to. Tickets are copied by
// value; their slices are shared, which is safe because the fold only
// ever replaces whole fields.
func (s *Snapshot) working() *Snapshot {
	w := *s
	w.Tickets = make([]*Ticket, len(s.Tickets))
	for i, t := range s.Tickets {
		c := *t
		w.Tickets[i] = &c
	}
	// Copied too, and not an afterthought: fold writes it, and a shared
	// map would have the pass rewriting the caller's record of what the
	// pipeline has done.
	w.Recorded = make(map[string]RecordedMove, len(s.Recorded))
	for k, v := range s.Recorded {
		w.Recorded[k] = v
	}
	return &w
}

// fold applies planned transitions to the working snapshot so later rules
// read the state this pass is producing.
//
// The synthesised Transition is stamped RoleControlPlane, which is what
// the sweep's own writes really are — and it keeps revertFor from judging
// a transition the sweep itself just planned.
func (w *Snapshot) fold(acts []Action, moved map[string]bool) {
	for _, a := range acts {
		switch a.Kind {
		case ActTransition:
			moved[a.TicketID] = true
			t := w.ticket(a.TicketID)
			if t == nil {
				continue
			}
			t.Last = &Transition{From: t.State, To: a.To, Actor: RoleControlPlane, At: w.Now}
			// The record moves with the state. A later rule reading a
			// folded ticket must see the sweep's own move as the sweep's,
			// not as a divergence from a record that no longer describes
			// it — which is what the real store will hold once Execute
			// writes the same thing ahead of the same transition.
			w.Recorded[a.TicketID] = RecordedMove{From: t.State, To: a.To, Role: RoleControlPlane}
			t.State = a.To
			t.StateSince = w.Now
		case ActRemoveLabel:
			t := w.ticket(a.TicketID)
			if t == nil {
				continue
			}
			kept := make([]string, 0, len(t.Labels))
			for _, l := range t.Labels {
				if l != a.Label {
					kept = append(kept, l)
				}
			}
			t.Labels = kept
		}
	}
}

// pipelineDepth ranks a state by how far along the pipeline it is, so a
// pass considers the furthest-along tickets first. The numbers are only
// ever compared, never stored.
//
// Blocked sits at the bottom rather than beside the state it came from:
// nothing the sweep does moves it, so considering it early would buy
// nothing, and the tickets that *are* moving are the ones whose effects
// the rest of the pass wants to see.
func pipelineDepth(s protocol.State) int {
	switch s {
	case protocol.Merged:
		return 100
	case protocol.Reconciling:
		return 90
	case protocol.Checks:
		return 80
	case protocol.Reworking:
		return 70
	case protocol.ReadyForRework:
		return 60
	case protocol.InProgress:
		return 50
	case protocol.ReadyForDev:
		return 40
	case protocol.DesignReview:
		return 30
	case protocol.Designing:
		return 20
	case protocol.Todo:
		return 10
	case protocol.Backlog:
		return 5
	case protocol.Blocked:
		return 1
	}
	return 0
}

// revertFor enforces the §9 invariants on how the ticket arrived in its
// current state: detect-and-revert, because the tracker has no
// pre-transition hook. Only the last transition is judged — once reverted,
// the revert becomes the last transition and the rule cannot re-fire.
func revertFor(s *Snapshot, t *Ticket) ([]Action, bool) {
	last, known := arrival(s, t)
	switch {
	case !known:
		// Nothing recorded for this ticket, so there is no arrival this
		// sweep can attribute. Not judged — see Snapshot.Recorded.
		return nil, false
	case last.Actor == RoleControlPlane:
		// The sweep trusts its own writes, or reverts would oscillate.
		// Meaningful now that the role comes from the record rather than
		// from a tracker identity the pipeline shares with the author.
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
		if other, mine := MutexHolder(s, t); other != nil {
			return revert("mutex",
				fmt.Sprintf("Mutex label %q is already in flight on %s. Two in-flight tickets may not share a screen or a system (DESIGN §6).", mine, other.Key)), true
		}
	}

	// Writer matrix: who may write which state (DESIGN §3, §9).
	if rule, prose, bad := writerViolation(t, last); bad {
		return revert(rule, prose), true
	}
	return nil, false
}

// arrival is the transition this sweep judges, and who made it.
//
// It reads the pipeline's own record rather than the tracker's history,
// because the tracker cannot answer the question. On a solo workspace
// the harness holds the author's API key, so every pipeline write is
// stamped with the author's identity — and resolving a role from that
// identity returned "control plane" for the human's moves too, which is
// the one role these rules trust. Every §9 invariant was off, silently,
// and a ticket promoted past a held mutex stood because of it.
//
// So the pipeline says what it did, and the tracker says where the
// ticket is. Two facts, and the comparison is the answer:
//
//   - the record's destination is where the ticket is → the pipeline
//     made the last move, and the record carries the edge and the role
//   - it is not → someone else moved it, from where the pipeline left it
//     to where it now is, and on a solo workspace that someone is the
//     author
//
// Note what this is not: it is not "the pipeline's moves are legal".
// An agent's move is recorded with the agent's role and then judged like
// any other — a design pass promoting straight past Design review is
// precisely what §9 exists to catch, and it is the pipeline doing it.
func arrival(s *Snapshot, t *Ticket) (*Transition, bool) {
	rec, ok := s.Recorded[t.ID]
	if !ok {
		return nil, false
	}
	if rec.To == t.State {
		return &Transition{From: rec.From, To: rec.To, Actor: rec.Role, At: t.StateSince}, true
	}
	return &Transition{From: rec.To, To: t.State, Actor: RoleAuthor, At: t.StateSince}, true
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

// block builds the one shape a transition into Blocked may take, and it
// is the only place in this file that transitions to it — a test holds
// that, because the point is that no future path can forget the part
// below.
//
// Only the author moves a ticket out of Blocked and they choose the
// state (DESIGN §12). Choosing needs knowing where it came from, and a
// ticket parked mid-flight does not announce that the way a needs-review
// ticket does: needs-review sits at the end of the line with one obvious
// destination, while "Reworking, until someone sets a secret" has to be
// remembered. So every arrival stamps `from` on whatever marker the
// transition already carries, and gets a bare `blocked` marker when it
// carries none.
func block(t *Ticket, m *marker.Marker, prose, reason string) []Action {
	if m == nil {
		m = &marker.Marker{Kind: marker.Blocked, Fields: map[string]string{}}
	}
	if m.Fields == nil {
		m.Fields = map[string]string{}
	}
	m.Fields["from"] = string(t.State)
	return []Action{{
		Kind: ActTransition, TicketID: t.ID, To: protocol.Blocked, Marker: m,
		Prose: prose, Reason: reason,
	}}
}

// conflictBounce sends a ticket whose branch conflicts back to the queue.
//
// Deliberately the same marker and the same argument as reconcile's
// bounce (internal/agent/reconcile.go), because it is the same event
// seen from a different place: a branch that went stale while the ticket
// was in flight. Sharing the marker is what keeps the third-conflict
// escalation counting across both — three conflicts on one ticket is a
// sequencing problem whichever half of the pipeline noticed them, and
// two separate counters would each stop at two.
//
// Back to the queue rather than straight to Reworking, for the reason
// the writer matrix gives (DESIGN §9): Reworking is the dev agent's own
// claim, and a state written by anyone else is reverted.
func conflictBounce(t *Ticket) []Action {
	attempt := len(markersOf(t, marker.MergeConflict)) + 1
	m := &marker.Marker{Kind: marker.MergeConflict, Fields: map[string]string{
		"attempt": fmt.Sprintf("%d", attempt),
		// Where it was seen. The two detection points have different
		// evidence behind them — this one never got a CI verdict at all
		// — and a marker that hides which is which makes the history
		// unreadable.
		"at": "checks",
	}}
	if t.CI.PRNumber > 0 {
		m.Fields["pr"] = fmt.Sprintf("%d", t.CI.PRNumber)
	}
	// The newest comment is the scope (DESIGN §2.3), so it has to say
	// plainly that the work is not what is wrong. A dev agent handed a
	// bounce reads it as a finding about the diff unless told otherwise,
	// and would start re-litigating a design that nothing has questioned.
	prose := "This branch conflicts with something that merged into main while the ticket was in flight. " +
		"CI cannot run on it at all — GitHub builds no merge commit for a conflicted pull request, so no verdict was coming and the ticket would have sat here.\n\n" +
		"**Nothing about the work is in question.** Do not revisit the design, the argument or the diff. " +
		"The scope is exactly this: merge `origin/main` into the branch, resolve the conflicts, keep both sides' intent, run the quality gates, and finish. " +
		"If a conflict cannot be resolved without changing what this ticket decided, that is a push-back rather than a guess (DESIGN §2.4)."
	return []Action{{
		Kind: ActTransition, TicketID: t.ID, To: protocol.ReadyForRework, Marker: m,
		Prose:  prose,
		Reason: "branch conflicts with main; no CI verdict can arrive",
	}}
}

// ciFor turns CI results on a Checks ticket into transitions (DESIGN §12,
// §13). In production the project stub delivers these event-driven; the
// sweep computes the same answers so the polled loop backstops lost events.
func ciFor(s *Snapshot, t *Ticket) []Action {
	if t.State != protocol.Checks || t.IsBoundary() {
		return nil
	}
	// A conflicted branch is checked first, and before CI, because it is
	// the reason there is no verdict to wait for. GitHub cannot build
	// the merge ref for a conflicted PR, so the `pull_request` run never
	// starts — the ticket does not fail here, it stops. Checks has no
	// agent and so no stale-claim timeout, so nothing else would ever
	// move it.
	//
	// This catches the green case too, which reconcile's bounce would
	// also have caught: promoting a branch that cannot merge spends a
	// full model-driven reconcile pass to arrive at the same state.
	// Reconcile's bounce stays, for the case no snapshot can see — main
	// moving between the read and the merge attempt.
	//
	// Except when CI is red: then a run did happen and did report, the
	// scope naming the failing jobs is the more specific one, and it
	// sends the ticket to the same place. Resolving the conflict is part
	// of that rework either way.
	if t.CI.Mergeable == MergeConflicted && t.CI.Status != CIRed {
		return conflictBounce(t)
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
			return block(t, m,
				"Second CI failure on this branch. Two reds is rarely a flake — the author decides whether this is scope, design, or infrastructure (DESIGN §12).",
				"CI red twice on the same branch")
		}
		prose := "CI failed. This comment is the newest, so it is the scope (DESIGN §2.3)."
		if len(t.CI.FailedJobs) > 0 {
			// Named, not just linked. The agent picking this up cannot
			// open the run — it holds no GitHub credential (DESIGN §9) —
			// so a bare link is a scope it can read and not act on. The
			// failing logs reach it separately, at claim.
			prose += "\n\nFailing: " + strings.Join(t.CI.FailedJobs, ", ") + "."
			m.Fields["jobs"] = strings.Join(t.CI.FailedJobs, ",")
		}
		return []Action{{
			Kind: ActTransition, TicketID: t.ID, To: protocol.ReadyForRework, Marker: m,
			Prose:  prose,
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
	// A branch that cannot land three times is not a stale branch any
	// more: main is moving faster than this ticket can, and the fix is
	// sequencing, which is the author's. Deliberately looser than the
	// reconcile-bounce rule at two — that one counts findings about the
	// work, and this one counts other people's merges, which is not the
	// ticket's fault and should not be punished at the same rate. Some
	// bound is needed all the same: without one, an active main and a
	// slow ticket loop between Reconciling and the queue forever, with
	// each pass burning a full agent run.
	if t.State == protocol.ReadyForRework && len(markersOf(t, marker.MergeConflict)) >= 3 {
		return block(t,
			&marker.Marker{Kind: marker.MergeConflict, Fields: map[string]string{"escalated": "true"}},
			"This branch has failed to merge three times: every reconciliation passed and every merge hit a conflict with work that landed first. That is a sequencing problem rather than a problem with the ticket — hold the competing work, or land this by hand (DESIGN §12).",
			"third merge conflict")
	}

	if t.State == protocol.ReadyForRework && len(markersOf(t, marker.ReconcileBounce)) >= 2 {
		return block(t, nil,
			"Second bounce from reconciliation on the same ticket. This is a design problem, not an implementation one — Designing is the usual route from here (DESIGN §12).",
			"second reconcile bounce")
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
			return block(t, nil,
				"Deployed clean, but reconciliation could not tell whether this landed as asked (needs-review). A human look closes it (DESIGN §11).",
				"deployed with needs-review")
		}
		return []Action{{
			Kind: ActTransition, TicketID: t.ID, To: protocol.Done,
			Reason: "deployed and clean (DESIGN §11)",
		}}
	case DeployFailed:
		return block(t, nil,
			"The deployment carrying this merge failed. Recovery is a redeploy, not a state change, so the author decides where this goes (DESIGN §12).",
			"deployment failed")
	default:
		if s.DeployTimeout > 0 && s.Now.Sub(t.StateSince) > s.DeployTimeout {
			return block(t, nil,
				"Merged past the deploy timeout with no deployment covering it — merged but never deployed is otherwise invisible (DESIGN §12).",
				"deploy timeout")
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
	// `run` only: the state this died in is `from`, which block stamps —
	// it used to be a second field named `state` saying the same thing,
	// and one fact under two names is the beginning of them disagreeing.
	m := &marker.Marker{Kind: marker.StaleClaim, Fields: map[string]string{"run": t.Run.ID}}

	// The run correlated to a ticket is the newest of *any* kind, while
	// the kind expected here comes from the state. When they disagree,
	// the honest reading is that this state's agent was never dispatched
	// at all — and saying "the reconcile run is no longer live" about a
	// dev run sends the reader looking for a crash that never happened.
	//
	// That is not hypothetical: a project whose reconcile workflow file
	// was invalid YAML had every dispatch rejected by GitHub, and the
	// ticket reported a dead reconcile run whose id belonged to the dev
	// pass that finished cleanly an hour earlier. The state was right,
	// the run was right, the sentence joining them was not.
	if t.Run.Kind != "" && t.Run.Kind != kind {
		m.Fields["dispatched"] = string(t.Run.Kind)
		return block(t, m, fmt.Sprintf(
			"No %s run was ever dispatched for this ticket. The newest run on it is the %s run `%s`, which ended before this state was entered — so the claim has nothing behind it and the grace period passed.\n\n"+
				"Look at the dispatch rather than at the run: the usual causes are the %s workflow file failing to parse (GitHub rejects the dispatch and shows the run named by its file path), a missing secret, or the workflow having been renamed.",
			kind, t.Run.Kind, t.Run.ID, kind), "stale claim, agent never dispatched")
	}
	return block(t, m,
		fmt.Sprintf("The %s run claiming this ticket is no longer live and the grace period passed. At one agent a stuck claim halts the queue, so it is detected rather than waited out (DESIGN §12).", kind),
		"stale claim")
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
			// The same question the pickup assertion asks. Asked here too
			// because the dispatcher used not to: it would send a ticket
			// whose screen or system was held straight into an assertion
			// that could only refuse, and since the refusal leaves the
			// ticket in the queue, it did it again on the next beat, and
			// the next. Each of those was a full job — checkout,
			// toolchain, services — spent to be told no.
			if other, _ := MutexHolder(s, t); other != nil {
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
