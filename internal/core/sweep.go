package core

import (
	"fmt"
	"sort"
	"strconv"
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

	// Record repair first, and outside the `unmoved` chain below. A
	// resync ticket is driven by nothing else in this function —
	// Unmanaged() takes it out of every rule — so the adopt is the whole
	// of what a sweep does for it, and doing it first means the record
	// is level from the same pass the label went on.
	for _, t := range tickets {
		plan(adoptFor(w, t))
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
	// Before the resolutions: the marker this writes is what the deploy
	// check reads to decide a Merged ticket landed, and postDeployFor is
	// that check's other half.
	unmoved(mergeBackfillFor)
	// A comment on a ticket nobody is moving. It is here rather than
	// beside the resolutions because it changes no state at all: Design
	// review is the author's, and this only makes what they are holding
	// actionable.
	unmoved(previewFor)
	// Resolution first: a deploy that landed retires the ticket, which
	// releases its mutex labels and clears it as a blocker for everything
	// judged after this point.
	unmoved(postDeployFor)
	unmoved(ciFor)
	unmoved(escalationsFor)
	unmoved(staleClaimFor)
	// After the resolutions, so a live-suite verdict that arrived on the
	// same beat as a deploy is judged against the state they leave.
	unmoved(liveSuiteFor)

	// The boundary ticket is created when the last milestone ticket
	// resolves, so it runs after the resolutions above rather than
	// against the state they replaced.
	plan(boundaryFor(w))

	// Promotion into the design queue, after both of those and for both
	// their reasons. After the resolutions, so a blocker that reached
	// Merged or Done on this pass counts as satisfied rather than being
	// read from the state it just left. After the boundary, so a
	// boundary ticket created moments ago pauses the queue immediately —
	// the alternative promotes one last ticket into a milestone that is
	// closing, which is precisely the scope change the pause exists to
	// prevent (DESIGN §8, §10).
	plan(promotionFor(w, moved))

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
		case ActAdopt:
			t := w.ticket(a.TicketID)
			if t == nil {
				continue
			}
			// No origin — the pipeline did not make this move — and no
			// entry in `moved`, because adopting is not moving. Folded
			// all the same, or a convergent sweep plans the same adopt
			// on every pass and never reaches a fixpoint.
			w.Recorded[a.TicketID] = RecordedMove{To: t.State, Role: RoleControlPlane}
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
	case protocol.ReadyForRedesign:
		return 16
	case protocol.ReadyForDesign:
		return 15
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
	case t.Unmanaged():
		// An author-only ticket is the author's from Todo to Done, and
		// the matrix below has no row for that: it would revert their
		// close as a done-writer violation, every sweep, because the
		// author moving it again is another arrival to judge (DESIGN §8).
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
	// except into Ready for design, which is where the flag gets folded
	// into the pass that follows rather than fought (DESIGN §7's
	// Backlog/Todo row). It was Designing until the queue and the
	// agent's own state were separated; the exception belongs to
	// whichever one the author moves a ticket into.
	if t.HasLabel(LabelReEvaluate) && forward(last.From, last.To) && last.To != protocol.ReadyForDesign {
		return revert("re-evaluate",
			"This ticket carries re-evaluate: an unresolved collision. It cannot move forward until the owning thread clears the label."), true
	}

	// Writer matrix: who may write which state (DESIGN §3, §9).
	if rule, prose, bad := writerViolation(t, last); bad {
		return revert(rule, prose), true
	}
	return nil, false
}

// mergeBackfillFor writes the `merged` marker for a merge the pipeline
// did not make.
//
// The marker is written at exactly one place otherwise — reconcile, once
// it has merged the PR itself — so a PR the *author* merges by hand
// leaves the ticket carrying no marker at all, and three readers then
// degrade in silence rather than failing: the retro note records the
// ticket with no commit, the rehearsal reset cannot revert what the note
// does not name, and the deploy check finds no SHA to compare, leaves
// the ticket Pending and lets the deploy timeout escalate a ticket that
// shipped fine to Blocked.
//
// Design-only tickets are the systematic case, because there is no dev
// pass and so no reconcile to reach: on Catapult's tech-debt milestone
// six of twenty-six retro entries carry no SHA, and five of those six
// touched only design-owned paths.
//
// Convergent, and that is what keeps it from re-firing: the comment it
// plans is the marker the next snapshot reads, so the ticket stops
// qualifying the moment the write lands. A failed write costs a beat.
func mergeBackfillFor(_ *Snapshot, t *Ticket) []Action {
	if len(t.HandMerges) == 0 {
		return nil
	}
	var acts []Action
	for _, hm := range t.HandMerges {
		fields := map[string]string{"sha": hm.SHA}
		if hm.PR != "" {
			fields["pr"] = hm.PR
		}
		acts = append(acts, Action{
			Kind:     ActComment,
			TicketID: t.ID,
			Marker:   &marker.Marker{Kind: marker.Merged, Fields: fields},
			Prose: "This landed as a merge the pipeline did not make, so the record of it was missing. " +
				"Recording it now, so the retro note, the rehearsal reset and the deploy check can all see the commit (DESIGN §11).",
			Reason: "merge landed outside the pipeline; backfilling the marker (DESIGN §11)",
		})
	}
	return acts
}

// previewFor announces the branch preview on a ticket in Design review, or
// says once that it is not coming.
//
// Design review is the author reading the rendered states, and DESIGN §4
// has always said the ticket asking for that review should say where to
// find them. The publisher used to be the design job itself, which knew
// the URL the moment it had published and put it on the ticket in the same
// breath as the transition. It no longer publishes: the preview is the
// deploy platform's, built out-of-band from the PR, so at finish there is
// nothing to announce yet and the announcement moved here.
//
// Three outcomes, and only two of them write:
//
//   - ready: the URL goes on the ticket. Terminal — a url-bearing marker
//     is what stops this rule looking, so it cannot re-fire.
//   - failed: said once. Not terminal, because a preview that failed and
//     was rebuilt still deserves announcing, so the suppression is a
//     failed marker rather than any marker.
//   - pending: nothing. A preview that is merely still building is not
//     news, and a comment per sweep saying so would be.
//
// Pending has a bound, and it is deploy.timeout rather than a number
// invented here. The question is the same one that timeout already
// answers — how long to wait for a platform to finish before saying it
// will not — and a threshold nobody has measured is the invention
// CLAUDE.md records surviving review once. Borrowed rather than
// duplicated, so a project that finds it wrong has one key to change.
//
// Not a Blocked flavour. Design review already hands the author the ball;
// this makes the ball they hold one they can act on.
func previewFor(s *Snapshot, t *Ticket) []Action {
	if t.State != protocol.DesignReview || HasPreviewURL(t) {
		return nil
	}
	switch t.Preview.State {
	case PreviewReady:
		if t.Preview.URL == "" {
			// The host promises a URL with a ready preview, so this is
			// unreachable rather than tolerated. Announcing it anyway
			// would post the dead link §4 chose silence over.
			return nil
		}
		return []Action{{
			Kind:     ActComment,
			TicketID: t.ID,
			Marker:   &marker.Marker{Kind: marker.Preview, Fields: map[string]string{"url": t.Preview.URL}},
			Prose: "Preview for this pass: " + t.Preview.URL + "\n\n" +
				"This is what Design review reads — the rendered states, alongside the doc diff on the PR.",
			Reason: "preview is up; announcing it on the ticket (DESIGN §4)",
		}}
	case PreviewFailed:
		return previewNotComing(t, "The preview build failed, so there are no rendered states to read for this pass.", t.Preview.Why)
	case PreviewPending:
		if s.Now.Sub(t.StateSince) < s.DeployTimeout {
			return nil
		}
		return previewNotComing(t,
			"The preview has not finished building since this ticket reached Design review, which is longer than the deploy timeout allows a platform.",
			t.Preview.Why)
	}
	return nil
}

// previewNotComing is the one comment that says a preview is not arriving,
// suppressed by its own output so it is said once rather than every sweep.
func previewNotComing(t *Ticket, what, why string) []Action {
	if hasMarkerField(t, marker.Preview, "state", "failed") {
		return nil
	}
	prose := what + " The doc diff on the PR is still reviewable; this says so rather than leaving you waiting on a link that is not coming."
	if why != "" {
		prose += "\n\nThe platform said: " + why
	}
	return []Action{{
		Kind:     ActComment,
		TicketID: t.ID,
		Marker:   &marker.Marker{Kind: marker.Preview, Fields: map[string]string{"state": "failed"}},
		Prose:    prose,
		Reason:   "preview is not coming; saying so rather than leaving the review waiting (DESIGN §4)",
	}}
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
	// A record with no origin cannot be judged, and must not be. The
	// writer matrix judges *edges* — "arrived at Ready for dev" is not a
	// rule, "arrived at Ready for dev from Designing" is — and a revert
	// sends the ticket back to that origin. With the origin missing, the
	// matrix reads a half-edge and the revert targets nothing: the sweep
	// planned `transition <id> -> ""` and Execute died on "no tracker
	// state for \"\"", taking the whole pass with it. One unjudgeable
	// ticket stopped every other ticket in the project from moving.
	//
	// So it is treated exactly as an absent record is, which is the rule
	// this file already relies on: what the pipeline has not recorded, it
	// does not judge. Ingest writes records with no origin deliberately —
	// nothing is known about where an adopted ticket came from — and this
	// is the same fact arriving by a different route.
	if rec.To == "" || rec.From == "" && rec.To == t.State {
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
	case protocol.Designing:
		// Designing is the design agent's claim, exactly as In progress
		// is the dev agent's. It could not be judged this way while the
		// author moved tickets into it to queue them: the state meant
		// both "queued" and "claimed", so no writer rule could be true
		// of it. Ready for design is what separated the two.
		if last.Actor != RoleDesign {
			return deny("claim",
				"Designing is written by the design agent as its claim, from Ready for design. Queue a ticket by moving it to Ready for design instead.")
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

	// A ticket in an agent's own state that no agent ever claimed goes to
	// the queue that feeds that agent.
	//
	// The hole this closes is narrow and reachable. An agent state is
	// written by a claim, so a hand-move into one is a §9 violation and
	// gets reverted — but only if the pipeline has a record of the
	// ticket to judge the arrival against, and "no record means not
	// judged" is deliberate (§9: treating an absent record as an author
	// move would revert the whole backlog on the first sweep). So a
	// ticket created and dragged straight into Designing before the
	// pipeline had ever written to it was judged by nothing, dispatched
	// by nothing — no agent state is a dispatch source — and timed out
	// by nothing, since the stale-claim rule needs a run to have died.
	// It simply sat.
	//
	// Both conditions are what keeps this from overlapping the rules
	// that already work. A record means revertFor owns it and sends it
	// back to where it came from, which is the more precise answer. A
	// run means an agent is either working (leave it) or dead (the
	// stale-claim rule's), and neither is this.
	if q, ok := queueFeeding(t); ok && t.Run == nil && !t.IsBoundary() && !t.Unmanaged() {
		if _, known := arrival(s, t); !known {
			acts = append(acts, Action{
				Kind: ActTransition, TicketID: t.ID, To: q,
				Reason: "no run ever claimed this and the pipeline has no record of it, so it is queued rather than left in a state that says an agent is working (DESIGN §9)",
			})
		}
	}

	// re-evaluate is cleared automatically on entry to the design queue
	// from Backlog or Todo — nothing was built, the pass that follows
	// absorbs it (DESIGN §7).
	if t.State == protocol.ReadyForDesign && t.HasLabel(LabelReEvaluate) && t.Last != nil &&
		(t.Last.From == protocol.Backlog || t.Last.From == protocol.Todo) {
		acts = append(acts, Action{
			Kind: ActRemoveLabel, TicketID: t.ID, Label: LabelReEvaluate,
			Reason: "re-evaluate clears on entry to the design queue; the pass folds it in (DESIGN §7)",
		})
	}

	// A flagged ticket in Design review returns to Designing: nothing is
	// built and revision is cheap (DESIGN §7).
	if t.State == protocol.DesignReview && t.HasLabel(LabelReEvaluate) {
		acts = append(acts, Action{
			Kind: ActTransition, TicketID: t.ID, To: protocol.ReadyForDesign,
			Reason: "re-evaluate on Design review returns the ticket to the design queue (DESIGN §7)",
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
	// Author-only work bypasses the gates entirely (DESIGN §8). If one
	// is sitting in Checks the author put it there by hand; dispatching
	// reconcile against it would spend a model pass judging a diff no
	// agent wrote against a scope no agent was given.
	if t.Unmanaged() {
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
		// A re-evaluate flag does not hold promotion. It used to, and
		// the hold was a dead end: Checks has no agent, so nothing in
		// that state ever evaluated the flag, and a green ticket sat
		// waiting on a human. Reconciliation is the thread that owns
		// the next state and it is about to read the diff against the
		// argument anyway — asking it one more question is cheaper than
		// stopping the pipeline until somebody notices (DESIGN §7).
		//
		// Reconcile is what merges, so nothing has been given up by
		// letting the ticket through: the flag travels with it and the
		// verdict decides.
		//
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
		if t.CI.RunURL == "" || alreadyReportedCIRed(t, t.CI.RunURL, t.CI.RunAttempt) {
			// Already recorded (or unidentifiable); the earlier sweep
			// moved the ticket. Nothing new to say.
			return nil
		}
		attempt := len(markersOf(t, marker.CIRed)) + 1
		m := &marker.Marker{Kind: marker.CIRed, Fields: map[string]string{
			"run": t.CI.RunURL,
			// Two different counts, and conflating them is the bug this
			// separates. `attempt` counts failures on this branch and
			// escalates at two; `run_attempt` is GitHub's attempt number
			// on the one run, which a re-run increments while the id and
			// the URL stay put.
			"run_attempt": fmt.Sprintf("%d", normalizeRunAttempt(t.CI.RunAttempt)),
			"attempt":     fmt.Sprintf("%d", attempt),
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
	// A branch that cannot land three times is not a stale branch any
	// more: main is moving faster than this ticket can, and the fix is
	// sequencing, which is the author's. Deliberately looser than the
	// reconcile-bounce rule at two — that one counts findings about the
	// work, and this one counts other people's merges, which is not the
	// ticket's fault and should not be punished at the same rate. Some
	// bound is needed all the same: without one, an active main and a
	// slow ticket loop between Reconciling and the queue forever, with
	// each pass burning a full agent run.
	//
	// Counted against what has already been escalated rather than against
	// the state, for the reason the bounce rule below gives: the author
	// is free to send this back to Ready for rework and the conflicts do
	// not go away when they do. Here that mattered twice over, because
	// the escalation used to post its own marker under `merge-conflict` —
	// the kind it was counting — so one escalation took a ticket from
	// three conflicts to four and the rule re-read its own output as a
	// fourth. It posts a `blocked` marker now.
	if n := len(markersOf(t, marker.MergeConflict)); t.State == protocol.ReadyForRework &&
		n >= 3 && !alreadyEscalatedAt(t, "conflicts", n) {
		return block(t, &marker.Marker{Kind: marker.Blocked, Fields: map[string]string{
			"conflicts": strconv.Itoa(n),
		}},
			"This branch has failed to merge three times: every reconciliation passed and every merge hit a conflict with work that landed first. That is a sequencing problem rather than a problem with the ticket — hold the competing work, or land this by hand. Sending it back to `Ready for rework` for another attempt is a legitimate answer too, and it will not be escalated again unless a fourth conflict lands (DESIGN §12).",
			"third merge conflict")
	}

	// Second bounce from reconciliation: the queue ticket carries two
	// bounce markers, and two failures to land the same scope is more
	// often a design problem than an implementation one.
	//
	// Counted against what has already been escalated, not against the
	// state alone. The state alone made this a trap: the author returning
	// the ticket to Ready for rework — a state DESIGN §12 explicitly
	// leaves them free to choose — left the two markers in place, so the
	// next sweep re-read them and blocked it again. See
	// alreadyEscalatedAt for the ticket that measured it.
	if n := len(markersOf(t, marker.ReconcileBounce)); t.State == protocol.ReadyForRework &&
		n >= 2 && !alreadyEscalatedAt(t, "bounces", n) {
		return block(t, &marker.Marker{Kind: marker.Blocked, Fields: map[string]string{
			"bounces": strconv.Itoa(n),
		}},
			"Second bounce from reconciliation on the same ticket. Two failures to land the same scope is more often a design problem than an implementation one, so `Ready for design` is the usual route from here — but that is a recommendation, not a routing. Sending this back to `Ready for rework` for another pass is a legitimate answer, and it will not be escalated again unless reconciliation bounces it a third time (DESIGN §12).",
			"second reconcile bounce")
	}

	// Second decline from the record review on the same ticket: the
	// design pass wrote pass narration into the record, was sent back
	// with the passages named, and wrote it again. A reviewer and a
	// writer that disagree twice running are not going to settle it on
	// a third pass — either the finding is a false positive the writer
	// is right to keep, or the prompt is not landing — and both are the
	// author's to look at, not another dispatch's (DESIGN §4, §12).
	//
	// Counted the way the bounce rule is, and for its reason: the
	// author may return the ticket to Ready for design for one more
	// pass, the two declines do not go away when they do, and the
	// escalation must not undo that choice on the next tick. It fires
	// once per count and records the count on the `blocked` marker.
	// Either design queue: the decline lands the ticket in Ready for
	// redesign, and the author may return it to Ready for design.
	if n := recordDeclines(t); (t.State == protocol.ReadyForRedesign || t.State == protocol.ReadyForDesign) &&
		n >= 2 && !alreadyEscalatedAt(t, "declines", n) {
		return block(t, &marker.Marker{Kind: marker.Blocked, Fields: map[string]string{
			"declines": strconv.Itoa(n),
		}},
			"Second decline from the record review on the same ticket: the design pass was sent back for narrating passes or alternatives in the record, and the rewrite did it again. Two rounds of the same finding means either the reviewer is wrong to flag it — a reason attached to a rule is not narration, and the writer is allowed to keep one — or the design prompt is not landing the rule, and neither is a third pass's to settle. Read the two findings on this ticket and decide. Returning it to `Ready for design` is a legitimate answer, and it will not be escalated again unless the review declines it a third time (DESIGN §4, §12).",
			"second record-review decline")
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

// adoptFor keeps the move record level with a ticket the author is
// repairing by hand (DESIGN §9).
//
// The record is the pipeline's account of its own writes, so an author
// move it permits leaves the two diverged — and the writer matrix judges
// the *standing* divergence, not the move that caused it, so it re-fires
// every sweep until they agree. That is what makes a hands-off label
// alone useless for repair: suppressing the judgement changes nothing
// about the gap it will resume judging. Adopting closes the gap instead.
//
// Only when they actually differ, or every poll writes the record it
// just wrote. And `From` is left empty deliberately: the pipeline did
// not make this move and has no origin to claim, which is the same
// shape ingest writes for an adopted ticket and which arrival already
// declines to judge.
func adoptFor(s *Snapshot, t *Ticket) []Action {
	if !t.HasLabel(LabelResync) {
		return nil
	}
	if rec, ok := s.Recorded[t.ID]; ok && rec.From == "" && rec.To == t.State {
		return nil
	}
	return []Action{{
		Kind: ActAdopt, TicketID: t.ID, To: t.State,
		Reason: "resync: the author is repairing this ticket's state, so the record follows it (DESIGN §9)",
	}}
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
	// A conclusion the host reported settles what the grace period is
	// there to wait out, so it is answered now rather than in twenty
	// minutes.
	//
	// The grace exists to separate "the run died" from "the run is slow
	// to say it finished" — a distinction only time can draw when the
	// host offers nothing but `completed`. `failed` and `cancelled` draw
	// it directly: nothing further is coming from either. Waiting on
	// them is the grace period re-asking a question already answered,
	// and on a cancellation it is worse than idle — the author stopped
	// the run deliberately, and the pipeline spent the next twenty
	// minutes declining to notice.
	//
	// Only where the run is the kind this state expects. The run
	// correlated to a ticket is the newest of *any* kind, so on a
	// mismatch the conclusion describes some other run entirely and the
	// branch below has the honest reading — saying "you cancelled the
	// design run" about a cancelled dev run is the same class of false
	// sentence the mismatch branch itself exists to avoid.
	//
	// `succeeded` deliberately keeps the grace: a run that finished
	// cleanly and left the ticket in its claim may simply not have
	// written the move yet, which is the case the wait was built for.
	if t.Run.Kind == kind {
		if prose, reason, known := stoppedRun(t.Run.Outcome, kind); known {
			return block(t, &marker.Marker{Kind: marker.StaleClaim, Fields: map[string]string{
				"run":     t.Run.ID,
				"outcome": string(t.Run.Outcome),
			}}, prose, reason)
		}
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

// stoppedRun reports a run whose outcome means nothing further is
// coming, with the sentence to say about it.
//
// Same marker kind as every other stale claim, with the outcome as a
// field — following the mismatch branch above, which varies its prose
// on the same marker rather than minting a kind. The arrival is
// identical (Blocked, out of an agent state, because the claim has
// nothing behind it); only the diagnosis differs, and the diagnosis is
// exactly what the field and the prose carry.
//
// The prose matters more than usual here. The ordinary stale-claim
// sentence sends the reader looking for a workflow file that failed to
// parse or a missing secret, which is the right hunt for a claim that
// died silently and precisely the wrong one for a run the author
// stopped on purpose or one that ran and reported a failure. This
// file's own history is the argument: a stale-claim comment asserting
// something false about a run cost a wrong diagnosis once already.
func stoppedRun(outcome RunOutcome, kind AgentKind) (prose, reason string, known bool) {
	switch outcome {
	case OutcomeCancelled:
		return fmt.Sprintf(
			"The %s run claiming this ticket was cancelled, so nothing further is coming. Parked here rather than waited out, because the cancellation is already the decision (DESIGN §12).\n\n"+
				"Only you move a ticket out of Blocked, and you choose the state — any state, including Done.",
			kind), "run cancelled", true
	case OutcomeFailed:
		return fmt.Sprintf(
			"The %s run claiming this ticket failed, so nothing further is coming. Parked here rather than waited out, because the failure is already the answer (DESIGN §12).\n\n"+
				"The run's own logs carry the cause; nothing on this ticket does. Only you move it out of Blocked, and you choose the state — a retry means sending it back to the queue that feeds this agent.",
			kind), "run failed", true
	}
	return "", "", false
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
			if t.IsBoundary() || t.Unmanaged() || t.LiveRun("") {
				continue
			}
			switch {
			case t.State == protocol.ReadyForDesign || t.State == protocol.ReadyForRedesign:
				// Either queue, not the agent's state; the precedence
				// rank puts a redesign ahead of a fresh design in
				// `ordered`, so it is reached first. Dispatching from
				// Designing meant Designing said two things — "queued
				// for design" and "a design agent is working on this" —
				// and a run that died before claiming left the second
				// reading on a ticket nobody had started. No
				// awaitingDispatch guard is needed here for the same
				// reason the dev queue needs none: the claim moves the
				// ticket out, so a live run is never in this state.
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
	// relations, re-evaluate, and the boundary and author-only labels
	// (DESIGN §5, §7, §8, §10).
	if !devBusy(s) {
		for _, t := range ordered {
			if t.State != protocol.ReadyForDev && t.State != protocol.ReadyForRework {
				continue
			}
			if t.IsBoundary() || t.HasLabel(LabelBoundary) || t.HasLabel(LabelReEvaluate) ||
				t.Unmanaged() || t.LiveRun("") {
				continue
			}
			if blockedByOpen(s, t) {
				continue
			}
			// The same question the pickup assertion asks, through the
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
	if boundary != nil && boundary.State == protocol.InProgress && liveSuiteSatisfied(boundary) &&
		awaitingDispatchOf(boundary, AgentBoundary) && !s.agentBusy(AgentBoundary) && !openBlockerFor(s, boundary.ID) {
		acts = append(acts, Action{Kind: ActDispatch, TicketID: boundary.ID, Agent: AgentBoundary,
			Reason: "author signalled the manual pass is done (DESIGN §10)"})
	}

	// Live suite: dispatched once when the boundary ticket opens, before
	// the author's pass, so the pass reads real end-to-end results
	// (DESIGN §10). The run posts its own result marker; the marker is
	// what ends the loop. A dead run without one is the author's to
	// re-run — the sweep does not resurrect it, for the same reason
	// stale claims are detected rather than silently retried (§12).
	if boundary != nil && awaitingDispatchOf(boundary, AgentLiveSuite) && !s.agentBusy(AgentLiveSuite) {
		switch {
		case boundary.State == protocol.Todo && liveSuiteVerdict(boundary) == "":
			// The first run, before the author's pass, so the pass reads
			// a real end-to-end result.
			acts = append(acts, Action{Kind: ActDispatch, TicketID: boundary.ID, Agent: AgentLiveSuite,
				Reason: "boundary open: live suite before the author's pass (DESIGN §10)"})
		case boundary.State == protocol.InProgress && !liveSuiteSatisfied(boundary):
			// The retry, and the reason entering In progress is safe to
			// use as the retry trigger: the boundary agent is gated on
			// the same verdict, so this and that dispatch are mutually
			// exclusive and the agent cannot start on an unproven tree.
			//
			// Bounded by the author rather than by a counter. A failed
			// re-run parks the ticket in Blocked, and nothing dispatches
			// from there — so each retry costs one deliberate move back
			// to In progress. That is what keeps a suite failing for an
			// environmental reason from burning the milestone's budget
			// in a loop nobody asked for.
			acts = append(acts, Action{Kind: ActDispatch, TicketID: boundary.ID, Agent: AgentLiveSuite,
				Reason: "boundary re-entered In progress with the live suite unsatisfied (DESIGN §10)"})
		}
	}
	return acts
}

// promotionFor moves the head of the order into the design queue.
//
// One transition at most, because the queue is kept one deep: the design
// agent is singular (DESIGN §6), so depth buys no throughput, and what it
// costs is the ordering. Two tickets sitting in `Ready for design` are
// separated by the precedence rule alone, which at equal state falls
// through to age — so a ticket freed later by a merge can be older than
// one that has been startable all along, and would go first. A queue one
// deep cannot disagree with the report.
//
// No marker and no comment. Every other rule in this file that posts one
// is reporting a judgment the author might dispute — a revert, an
// escalation, a stale claim. This is the pipeline doing the thing the
// order report already said it would do, on every ticket, forever; a
// comment per promotion would be the noisiest marker in the project and
// would say nothing the state history does not. The move is recorded and
// attributed like any control-plane write, which is what keeps §9 from
// judging it as an author move.
func promotionFor(s *Snapshot, moved map[string]bool) []Action {
	p := nextPromotion(s, moved)
	if p.Ticket == nil {
		return nil
	}
	return []Action{{
		Kind: ActTransition, TicketID: p.Ticket.ID, To: protocol.ReadyForDesign,
		Reason: "head of the order, every blocker at Merged or later, design queue empty (DESIGN §8)",
	}}
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

// awaitingDispatchOf answers awaitingDispatch for one agent kind.
//
// The plain form asks "has any run been dispatched since this ticket
// entered its state", and that is exactly right wherever one state feeds
// one agent — which is everywhere except one place. The boundary
// ticket's `In progress` feeds two: the live suite re-runs there when
// its verdict is unproven, and the boundary agent runs there when it is
// (DESIGN §10).
//
// So a live-suite run that ended *after* the ticket arrived answered the
// boundary agent's question with "something already ran here", and the
// boundary agent never dispatched — on a green suite. Measured on
// Catapult's ORC-99: entered `In progress` at 19:23:37, the live suite
// passed at 19:26:09, and nothing dispatched afterwards. The ticket then
// sat until the stale-claim rule parked it, correctly reporting that no
// boundary run had ever been dispatched — the symptom named accurately
// by a rule that was not the cause.
//
// `Run` collapses to one run per ticket, so the kind on it is the whole
// of what can be asked. A run of another kind that is still live means
// the ticket is busy; one that has ended means this kind has still never
// been dispatched for this arrival.
func awaitingDispatchOf(t *Ticket, kind AgentKind) bool {
	if t.Run == nil {
		return true
	}
	if t.Run.Kind != kind {
		return !t.Run.Live
	}
	return awaitingDispatch(t)
}

// devBusy: the dev agent is singular — busy if any ticket holds a live dev
// run, or is sitting in a dev-owned state at all (a dead run there is the
// stale-claim rule's business, not a reason to double-dispatch).
//
// The state clause is a proxy for "a dev run is out there", so it only
// holds where a dev run could have put the ticket. Author-only tickets
// are excluded for the same reason boundary tickets are: no agent will
// ever be dispatched against one, and an author dragging theirs into In
// progress — the obvious thing to do while working on it — would
// otherwise freeze the whole dev queue until they closed it.
func devBusy(s *Snapshot) bool {
	for _, t := range s.Tickets {
		if t.LiveRun(AgentDev) {
			return true
		}
		if t.IsBoundary() || t.Unmanaged() {
			continue
		}
		if t.State == protocol.InProgress || t.State == protocol.Reworking {
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

// queueFeeding maps an agent's own state to the queue it is claimed
// from, for the states where that is a fact rather than a guess.
//
// Checks and Reconciling are absent on purpose. Neither is claimed from
// a queue — Checks is entered by the dev agent flipping the draft off
// and Reconciling by the control plane on CI green — so there is no
// queue to return a ticket to, and the nearest candidates would put one
// somewhere no PR backs it. A ticket hand-dropped into either still
// sits; it is a stranger place to drop one, and inventing a destination
// is worse than leaving it visible.
func queueFeeding(t *Ticket) (protocol.State, bool) {
	switch t.State {
	case protocol.Designing:
		// A design claimed out of Ready for redesign goes back there,
		// so the bounce it carries stays visible; the claim is the one
		// move the record holds for it.
		if t.Last != nil && t.Last.From == protocol.ReadyForRedesign {
			return protocol.ReadyForRedesign, true
		}
		return protocol.ReadyForDesign, true
	case protocol.InProgress:
		return protocol.ReadyForDev, true
	case protocol.Reworking:
		return protocol.ReadyForRework, true
	}
	return "", false
}

// liveSuiteVerdict is the newest live-suite result on a boundary ticket,
// or "" when none has been posted. Comments arrive oldest first, so the
// last marker is the newest run's.
func liveSuiteVerdict(t *Ticket) string {
	ms := markersOf(t, marker.LiveSuite)
	if len(ms) == 0 {
		return ""
	}
	return ms[len(ms)-1].Fields["result"]
}

// liveSuiteSatisfied reports whether the boundary agent may start.
//
// `no-tests` satisfies it, and that is the protocol rather than
// leniency: a project with no `:live` tests yet is not broken, and
// whether the milestone can close without live coverage is the author's
// call during their pass (DESIGN §10). Blocking on it would make the
// first boundary of every new project red for a structural reason, which
// is how a gate becomes one people learn to click past.
func liveSuiteSatisfied(t *Ticket) bool {
	switch liveSuiteVerdict(t) {
	case "pass", "no-tests":
		return true
	}
	return false
}

// liveSuiteUnreported reports a failing live suite this ticket has not
// already been sent to Blocked for.
//
// Positional, walking the comments once: a live-suite marker with no
// live-suite `blocked` marker after it is a verdict nobody has been told
// about.
//
// The alternative — block whenever the newest verdict is `fail` — takes
// the ticket away from the author. Their move out of Blocked is theirs
// to choose (DESIGN §12) and `Blocked` → `Todo` is the natural one, "seen
// it, back to my pass". That lands on a ticket whose newest verdict is
// still `fail`, so the next sweep parks it again, and again, and the
// author can never reach the state their own pass happens in.
//
// Not an oscillation in the sense the sim harness checks for, which is
// worth saying because that harness is what a reader would expect to
// catch this: each sweep settles, it just settles on undoing the author.
// Measured by removing this check — the scenario failed on a state
// assertion, "state = blocked, want todo", not on non-convergence.
//
// A re-run posts a new marker, which puts the newest verdict after the
// report again — so a second failure blocks a second time, which is the
// point.
func liveSuiteUnreported(t *Ticket) bool {
	verdict, reported := -1, -1
	for i, c := range t.Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok {
			continue
		}
		switch {
		case m.Kind == marker.LiveSuite:
			verdict = i
		case m.Kind == marker.Blocked && m.Fields["live-suite"] != "":
			reported = i
		}
	}
	return verdict >= 0 && reported < verdict
}

// liveSuiteFor parks the boundary ticket when the live suite fails.
//
// The failure used to be a comment and nothing else. A boundary ticket
// in `Todo` looked identical whether the suite had passed, failed, run
// no tests, or not run at all — four situations, one appearance — and
// §10's own reasoning for not surfacing it elsewhere was that the marker
// "lands on the boundary ticket where the author is already looking".
// That assumption did not hold: on Catapult's ORC-99 the failure sat
// unnoticed until somebody went and read the marker deliberately.
//
// `Blocked` rather than a label, because the state is the thing every
// listing shows and the thing the author already scans for. What makes
// it safe is that the boundary agent is gated on the verdict below, so
// clearing this the obvious way does not skip the manual pass: entering
// `In progress` re-runs the suite first.
func liveSuiteFor(s *Snapshot, t *Ticket) []Action {
	if !t.IsBoundary() || t.Resolved() {
		return nil
	}
	if t.State != protocol.Todo && t.State != protocol.InProgress {
		return nil
	}
	if liveSuiteVerdict(t) != "fail" || !liveSuiteUnreported(t) {
		return nil
	}
	// Only once the run has stopped. A re-run in flight is about to
	// replace this verdict, and parking the ticket underneath it would
	// tell the author to look at a result the pipeline is already
	// redoing.
	if t.LiveRun("") {
		return nil
	}
	return block(t,
		&marker.Marker{Kind: marker.Blocked, Fields: map[string]string{"live-suite": "1"}},
		"The live suite failed and the run has finished. Read it, then either fix what it found and come back, "+
			"or accept it and move on — moving this ticket to **Todo** resumes your manual pass, and moving it to "+
			"**In progress** re-runs the suite before the boundary agent starts (DESIGN §10).",
		"live suite failed (DESIGN §10)")
}
