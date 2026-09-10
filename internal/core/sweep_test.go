package core

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func snap(tickets ...*Ticket) *Snapshot {
	s := &Snapshot{
		Now:              t0,
		CurrentMilestone: "M1",
		StaleClaimGrace:  20 * time.Minute,
		DeployTimeout:    30 * time.Minute,
		Tickets:          tickets,
		Recorded:         map[string]RecordedMove{},
	}
	// arrived() states an arrival the way a reader thinks about it —
	// "came from X, moved by R". The sweep no longer reads that from the
	// tracker's history, so translate it into the pipeline's own record,
	// which is where the answer now comes from.
	for _, t := range tickets {
		if t.Last == nil {
			continue
		}
		if pipelineRole(t.Last.Actor) {
			// The pipeline made the move, so its record describes it.
			s.Recorded[t.ID] = RecordedMove{From: t.Last.From, To: t.Last.To, Role: t.Last.Actor}
			continue
		}
		// Somebody outside the pipeline made it, so the record stops
		// where the pipeline left the ticket and the tracker has moved on.
		s.Recorded[t.ID] = RecordedMove{To: t.Last.From, Role: RoleControlPlane}
	}
	return s
}

func pipelineRole(r Role) bool {
	switch r {
	case RoleDesign, RoleDev, RoleReconcile, RoleBoundary, RoleControlPlane, RoleCI:
		return true
	}
	return false
}

func tk(key string, state protocol.State, mut ...func(*Ticket)) *Ticket {
	t := &Ticket{
		ID: key, Key: key, State: state,
		StateSince: t0.Add(-time.Minute), CreatedAt: t0.Add(-time.Hour),
		Milestone: "M1", Priority: 3,
	}
	for _, m := range mut {
		m(t)
	}
	return t
}

func arrived(from protocol.State, actor Role) func(*Ticket) {
	return func(t *Ticket) {
		t.Last = &Transition{From: from, To: t.State, Actor: actor, At: t.StateSince}
	}
}

func withComment(kind marker.Kind, fields map[string]string) func(*Ticket) {
	return func(t *Ticket) {
		m := marker.Marker{Kind: kind, Fields: fields}
		t.Comments = append(t.Comments, Comment{Body: m.Format(), Actor: RoleControlPlane, At: t0})
	}
}

func find(acts []Action, kind ActionKind, ticketID string) *Action {
	for i := range acts {
		if acts[i].Kind == kind && acts[i].TicketID == ticketID {
			return &acts[i]
		}
	}
	return nil
}

func TestKillSwitchStopsEverything(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDev, arrived(protocol.DesignReview, RoleAuthor)))
	s.KillSwitch = true
	if acts := Sweep(s); len(acts) != 0 {
		t.Errorf("kill switch on, got actions: %v", acts)
	}
}

func TestSignOffByAuthorPasses(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDev, arrived(protocol.DesignReview, RoleAuthor)))
	acts := Sweep(s)
	if a := find(acts, ActTransition, "T1"); a != nil {
		t.Errorf("legitimate sign-off reverted: %v", *a)
	}
	if a := find(acts, ActDispatch, "T1"); a == nil || a.Agent != AgentDev {
		t.Errorf("queue head not dispatched to dev: %v", acts)
	}
}

func TestSignOffSkippingDesignReviewReverts(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDev, arrived(protocol.Designing, RoleDesign)))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Designing || a.Marker == nil || a.Marker.Fields["rule"] != "sign-off" {
		t.Errorf("want sign-off revert to Designing, got %v", a)
	}
}

func TestDecisionlessPassMayAdvanceDirectly(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDev,
		arrived(protocol.Designing, RoleDesign),
		withComment(marker.DecisionlessPass, nil)))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil {
		t.Errorf("decisionless pass reverted: %v", *a)
	}
}

func TestNonAuthorSignOffReverts(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDev, arrived(protocol.DesignReview, RoleDev)))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.DesignReview {
		t.Errorf("want revert of non-author sign-off, got %v", a)
	}
}

func TestScreenMutexRevertsCollidingPromotion(t *testing.T) {
	inFlight := tk("T1", protocol.InProgress, func(t *Ticket) {
		t.Labels = []string{"screen:home"}
		t.Run = &Run{ID: "r1", Kind: AgentDev, Live: true}
	})
	promoted := tk("T2", protocol.ReadyForDev,
		arrived(protocol.DesignReview, RoleAuthor),
		func(t *Ticket) { t.Labels = []string{"screen:home"} })
	a := find(Sweep(snap(inFlight, promoted)), ActTransition, "T2")
	if a == nil || a.To != protocol.DesignReview || a.Marker.Fields["rule"] != "mutex" {
		t.Errorf("want screen-mutex revert, got %v", a)
	}
}

func TestReEvaluateForwardTransitionReverts(t *testing.T) {
	s := snap(tk("T1", protocol.InProgress,
		arrived(protocol.ReadyForDev, RoleAuthor),
		func(t *Ticket) { t.Labels = []string{LabelReEvaluate} }))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.ReadyForDev || a.Marker.Fields["rule"] != "re-evaluate" {
		t.Errorf("want re-evaluate revert, got %v", a)
	}
}

func TestAuthorFromBlockedOverridesWriterMatrix(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForRework, arrived(protocol.Blocked, RoleAuthor)))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil && strings.HasPrefix(a.Reason, "invariant") {
		t.Errorf("author's unblock reverted: %v", *a)
	}
}

func TestHandMovedClaimReverts(t *testing.T) {
	s := snap(tk("T1", protocol.InProgress, arrived(protocol.ReadyForDev, RoleAuthor)))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.ReadyForDev || a.Marker.Fields["rule"] != "claim" {
		t.Errorf("want claim revert, got %v", a)
	}
}

func TestBacklogWithCurrentMilestoneMovesToTodo(t *testing.T) {
	s := snap(tk("T1", protocol.Backlog))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Todo {
		t.Errorf("want Backlog -> Todo correction, got %v", a)
	}
}

func TestCIGreenPromotesAndDispatchesReconcile(t *testing.T) {
	s := snap(tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIGreen, RunURL: "https://ci/1"}
	}))
	acts := Sweep(s)
	tr := find(acts, ActTransition, "T1")
	if tr == nil || tr.To != protocol.Reconciling {
		t.Fatalf("want promotion to Reconciling, got %v", acts)
	}
	if d := find(acts, ActDispatch, "T1"); d == nil || d.Agent != AgentReconcile {
		t.Errorf("want reconcile dispatch, got %v", acts)
	}
}

// re-evaluate no longer holds promotion out of Checks. The hold was a
// dead end: Checks has no agent, so nothing in that state evaluated the
// flag, and a green ticket waited on a human. Reconciliation owns the
// next state, is about to read the diff against the argument anyway,
// and is what merges — so the flag travels with the ticket and the
// verdict answers it (DESIGN §7).
func TestCIGreenPromotesEvenWhenFlagged(t *testing.T) {
	s := snap(tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIGreen}
		t.Labels = []string{LabelReEvaluate}
	}))
	acts := Sweep(s)
	a := find(acts, ActTransition, "T1")
	if a == nil || a.To != protocol.Reconciling {
		t.Fatalf("a flagged green ticket did not promote: %v", a)
	}
	if d := find(acts, ActDispatch, "T1"); d == nil || d.Agent != AgentReconcile {
		t.Errorf("no reconcile dispatch to answer the flag: %v", d)
	}
}

func TestCIRedFirstGoesToRework(t *testing.T) {
	s := snap(tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIRed, RunURL: "https://ci/9"}
	}))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.ReadyForRework || a.Marker.Fields["attempt"] != "1" {
		t.Errorf("want first red -> Ready for rework, got %v", a)
	}
}

func TestCIRedSecondBlocks(t *testing.T) {
	s := snap(tk("T1", protocol.Checks,
		withComment(marker.CIRed, map[string]string{"run": "https://ci/9", "attempt": "1"}),
		func(t *Ticket) { t.CI = CIInfo{Status: CIRed, RunURL: "https://ci/10"} }))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked || a.Marker.Fields["attempt"] != "2" {
		t.Errorf("want second red -> Blocked, got %v", a)
	}
}

func TestCIRedAlreadyRecordedIsSilent(t *testing.T) {
	s := snap(tk("T1", protocol.Checks,
		withComment(marker.CIRed, map[string]string{"run": "https://ci/9", "attempt": "1"}),
		func(t *Ticket) { t.CI = CIInfo{Status: CIRed, RunURL: "https://ci/9"} }))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil {
		t.Errorf("recorded red re-fired: %v", *a)
	}
}

func TestSecondReconcileBounceBlocks(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForRework,
		withComment(marker.ReconcileBounce, map[string]string{"n": "1"}),
		withComment(marker.ReconcileBounce, map[string]string{"n": "2"})))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want second bounce -> Blocked, got %v", a)
	}
	// The count rides on the marker, or the escalation cannot tell its
	// own work from a fresh bounce on the next sweep.
	if a.Marker == nil || a.Marker.Fields["bounces"] != "2" {
		t.Errorf("want the escalation to record bounces=2, got %v", a.Marker)
	}
}

// Only the author moves a ticket out of Blocked and they choose the state
// (DESIGN §12). Ready for rework is one of the states they may choose —
// sometimes the fix really is one more pass — and the escalation must not
// undo that choice on the next tick. Catapult's ORC-174 went Blocked ->
// Ready for rework -> Blocked in twenty-seven seconds, posting the same
// comment twice with no reconciliation between them.
func TestTheAuthorsReturnToReworkIsNotReEscalated(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForRework,
		withComment(marker.ReconcileBounce, map[string]string{"n": "1"}),
		withComment(marker.ReconcileBounce, map[string]string{"n": "2"}),
		withComment(marker.Blocked, map[string]string{"from": "ready_for_rework", "bounces": "2"})))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil && a.To == protocol.Blocked {
		t.Errorf("re-blocked the author's return with no new bounce: %v", *a)
	}
	// And the ticket is not merely left alone — it goes back to work,
	// which is the whole point of the author's choice.
	if a := find(Sweep(s), ActDispatch, "T1"); a == nil || a.Agent != AgentDev {
		t.Errorf("no dev run dispatched for the returned ticket: %v", Sweep(s))
	}
}

// A third bounce is new information, so it escalates again. Suppressing
// it would be the opposite failure: an author who tried once more and was
// bounced once more is owed the same stop the second bounce gave them.
func TestAThirdBounceEscalatesAgain(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForRework,
		withComment(marker.ReconcileBounce, map[string]string{"n": "1"}),
		withComment(marker.ReconcileBounce, map[string]string{"n": "2"}),
		withComment(marker.Blocked, map[string]string{"from": "ready_for_rework", "bounces": "2"}),
		withComment(marker.ReconcileBounce, map[string]string{"n": "3"})))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want a third bounce -> Blocked, got %v", a)
	}
	if a.Marker == nil || a.Marker.Fields["bounces"] != "3" {
		t.Errorf("want the escalation to record bounces=3, got %v", a.Marker)
	}
}

func TestPostDeploy(t *testing.T) {
	clean := tk("T1", protocol.Merged, func(t *Ticket) { t.Deploy = DeployDeployed })
	review := tk("T2", protocol.Merged, func(t *Ticket) {
		t.Deploy = DeployDeployed
		t.Labels = []string{LabelNeedsReview}
	})
	failed := tk("T3", protocol.Merged, func(t *Ticket) { t.Deploy = DeployFailed })
	stuck := tk("T4", protocol.Merged, func(t *Ticket) {
		t.Deploy = DeployPending
		t.StateSince = t0.Add(-31 * time.Minute)
	})
	acts := Sweep(snap(clean, review, failed, stuck))
	if a := find(acts, ActTransition, "T1"); a == nil || a.To != protocol.Done {
		t.Errorf("clean deploy: %v", a)
	}
	if a := find(acts, ActTransition, "T2"); a == nil || a.To != protocol.Blocked {
		t.Errorf("needs-review deploy: %v", a)
	}
	if a := find(acts, ActTransition, "T3"); a == nil || a.To != protocol.Blocked {
		t.Errorf("failed deploy: %v", a)
	}
	if a := find(acts, ActTransition, "T4"); a == nil || a.To != protocol.Blocked {
		t.Errorf("deploy timeout: %v", a)
	}
}

func TestStaleClaimBlocksAfterGrace(t *testing.T) {
	dead := tk("T1", protocol.InProgress, func(t *Ticket) {
		t.StateSince = t0.Add(-time.Hour)
		t.Run = &Run{ID: "r1", Kind: AgentDev, Live: false, EndedAt: t0.Add(-30 * time.Minute)}
	})
	a := find(Sweep(snap(dead)), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked || a.Marker.Kind != marker.StaleClaim {
		t.Errorf("want stale claim -> Blocked, got %v", a)
	}

	fresh := tk("T2", protocol.InProgress, func(t *Ticket) {
		t.StateSince = t0.Add(-time.Hour)
		t.Run = &Run{ID: "r2", Kind: AgentDev, Live: false, EndedAt: t0.Add(-10 * time.Minute)}
	})
	if a := find(Sweep(snap(fresh)), ActTransition, "T2"); a != nil {
		t.Errorf("inside grace, got %v", *a)
	}
}

// A conclusion the host reported answers the question the grace period
// is waiting to answer, so the wait is skipped. Catapult's ORC-181 sat
// in Designing after its design run was cancelled by hand, and every
// attempt to move it out was reverted — while the twenty-minute clock it
// was waiting on restarted with each attempt, because the grace measures
// from the later of the run's death and the state entry.
func TestAStoppedRunParksTheTicketWithoutWaiting(t *testing.T) {
	for _, c := range []struct {
		name    string
		outcome RunOutcome
		field   string
	}{
		{"cancelled", OutcomeCancelled, "cancelled"},
		{"failed", OutcomeFailed, "failed"},
	} {
		// Well inside the grace: the run ended a minute ago.
		tick := tk("T1", protocol.Designing, func(t *Ticket) {
			t.Run = &Run{ID: "r1", Kind: AgentDesign, Live: false,
				EndedAt: t0.Add(-time.Minute), Outcome: c.outcome}
		})
		a := find(Sweep(snap(tick)), ActTransition, "T1")
		if a == nil || a.To != protocol.Blocked {
			t.Fatalf("%s: want Blocked without waiting out the grace, got %v", c.name, a)
		}
		if a.Marker == nil || a.Marker.Fields["outcome"] != c.field {
			t.Errorf("%s: want outcome=%s recorded, got %v", c.name, c.field, a.Marker)
		}
		// The ordinary stale-claim prose sends the reader hunting a
		// dispatch that never happened, which is the wrong hunt here.
		if strings.Contains(a.Prose, "never dispatched") || strings.Contains(a.Prose, "missing secret") {
			t.Errorf("%s: prose blames the dispatch:\n%s", c.name, a.Prose)
		}
	}
}

// A clean finish keeps the grace. The run may simply not have written
// its move yet, which is the case the wait was built for.
func TestASucceededRunStillWaitsOutTheGrace(t *testing.T) {
	tick := tk("T1", protocol.Designing, func(t *Ticket) {
		t.Run = &Run{ID: "r1", Kind: AgentDesign, Live: false,
			EndedAt: t0.Add(-time.Minute), Outcome: OutcomeSucceeded}
	})
	if a := find(Sweep(snap(tick)), ActTransition, "T1"); a != nil {
		t.Errorf("parked a cleanly-finished run inside the grace: %v", *a)
	}
}

// An outcome nobody has measured must change nothing, or a widened
// adapter reaches a rule that has not been taught what the value means.
func TestAnUnmeasuredOutcomeChangesNothing(t *testing.T) {
	tick := tk("T1", protocol.Designing, func(t *Ticket) {
		t.Run = &Run{ID: "r1", Kind: AgentDesign, Live: false,
			EndedAt: t0.Add(-time.Minute), Outcome: OutcomeUnknown}
	})
	if a := find(Sweep(snap(tick)), ActTransition, "T1"); a != nil {
		t.Errorf("acted on an unmeasured outcome inside the grace: %v", *a)
	}
}

// The run correlated to a ticket is the newest of any kind, so a
// conclusion on the wrong kind describes some other run. Reading it as
// this state's claim produces exactly the false sentence the mismatch
// branch exists to avoid — and that branch, which names the dispatch,
// is the honest reading here.
func TestAnOutcomeOnAnotherKindOfRunIsNotThisClaims(t *testing.T) {
	// Designing expects a design run; the newest is a cancelled dev one.
	tick := tk("T1", protocol.Designing, func(t *Ticket) {
		t.Run = &Run{ID: "r1", Kind: AgentDev, Live: false,
			EndedAt: t0.Add(-time.Minute), Outcome: OutcomeCancelled}
	})
	if a := find(Sweep(snap(tick)), ActTransition, "T1"); a != nil {
		t.Errorf("read another kind's cancellation as this claim's: %v", *a)
	}
	// Past the grace it still parks, by the mismatch branch, saying so.
	old := tk("T2", protocol.Designing, func(t *Ticket) {
		t.StateSince = t0.Add(-time.Hour)
		t.Run = &Run{ID: "r2", Kind: AgentDev, Live: false,
			EndedAt: t0.Add(-30 * time.Minute), Outcome: OutcomeCancelled}
	})
	a := find(Sweep(snap(old)), ActTransition, "T2")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want the mismatch branch to park it, got %v", a)
	}
	if a.Marker.Fields["dispatched"] != string(AgentDev) {
		t.Errorf("want the mismatch branch's diagnosis, got %v", a.Marker)
	}
}

// author-only cannot be borrowed as a pass for one move, which is the
// obvious thing to reach for and was measured before resync was built.
// The label suppresses the judgement while it is on; the record does not
// move; taking it off re-exposes the identical divergence to the
// identical revert.
func TestAuthorOnlyIsNotAPassForOneMove(t *testing.T) {
	inDesigning := RecordedMove{From: protocol.ReadyForDesign, To: protocol.Designing, Role: RoleDesign}

	labelled := tk("T1", protocol.Done, func(x *Ticket) { x.Labels = []string{LabelAuthorOnly} })
	s := snap(labelled)
	s.Recorded["T1"] = inDesigning
	if a := find(Sweep(s), ActTransition, "T1"); a != nil {
		t.Errorf("reverted while labelled: %v", *a)
	}

	// The label comes off and the gap is still there to be judged.
	bare := tk("T1", protocol.Done)
	s = snap(bare)
	s.Recorded["T1"] = inDesigning
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Designing {
		t.Fatalf("want the revert once the label is off, got %v", a)
	}
}

// resync closes the gap instead of hiding it: the record follows the
// ticket, so there is nothing left to judge when the label comes off.
// This is ORC-181's repair, start to finish.
func TestResyncAdoptsTheTicketsStateIntoTheRecord(t *testing.T) {
	// Stuck: the pipeline thinks Designing, the author has moved it to
	// Done, and the writer matrix reverts that every sweep.
	tick := tk("T1", protocol.Done, func(x *Ticket) { x.Labels = []string{LabelResync} })
	s := snap(tick)
	s.Recorded["T1"] = RecordedMove{From: protocol.ReadyForDesign, To: protocol.Designing, Role: RoleDesign}

	acts := Sweep(s)
	if a := find(acts, ActTransition, "T1"); a != nil {
		t.Errorf("reverted a ticket under repair: %v", *a)
	}
	a := find(acts, ActAdopt, "T1")
	if a == nil || a.To != protocol.Done {
		t.Fatalf("want the record adopted at done, got %v", a)
	}
	// And with the record level, removing the label leaves nothing to
	// judge — the half author-only could never reach.
	settled := tk("T1", protocol.Done)
	s2 := snap(settled)
	s2.Recorded["T1"] = RecordedMove{To: protocol.Done, Role: RoleControlPlane}
	if a := find(Sweep(s2), ActTransition, "T1"); a != nil {
		t.Errorf("reverted after the repair settled: %v", *a)
	}
}

// Adopting is not a move, so it must stop once the record agrees or a
// convergent sweep never reaches a fixpoint.
func TestResyncAdoptsOnlyWhileTheRecordDisagrees(t *testing.T) {
	tick := tk("T1", protocol.Done, func(x *Ticket) { x.Labels = []string{LabelResync} })
	s := snap(tick)
	s.Recorded["T1"] = RecordedMove{To: protocol.Done, Role: RoleControlPlane}
	if a := find(Sweep(s), ActAdopt, "T1"); a != nil {
		t.Errorf("re-adopted a record that already agrees: %v", *a)
	}
}

// Hands off while the label is on, or an agent starts on a ticket being
// repaired mid-repair.
func TestResyncKeepsTheAgentsOff(t *testing.T) {
	queued := tk("T1", protocol.ReadyForDesign, func(x *Ticket) { x.Labels = []string{LabelResync} })
	if a := find(Sweep(snap(queued)), ActDispatch, "T1"); a != nil {
		t.Errorf("dispatched design against a ticket under repair: %v", *a)
	}
	dev := tk("T2", protocol.ReadyForDev, func(x *Ticket) { x.Labels = []string{LabelResync} })
	if a := find(Sweep(snap(dev)), ActDispatch, "T2"); a != nil {
		t.Errorf("dispatched dev against a ticket under repair: %v", *a)
	}
	// And a ticket in an agent state is not corrected back to its queue,
	// which would undo the very move being made.
	stuck := tk("T3", protocol.Designing, func(x *Ticket) { x.Labels = []string{LabelResync} })
	s := snap(stuck)
	delete(s.Recorded, "T3")
	if a := find(Sweep(s), ActTransition, "T3"); a != nil {
		t.Errorf("corrected a ticket under repair back to its queue: %v", *a)
	}
}

// The mutex is deliberately not released. The label repairs the record;
// it says nothing about the branch the ticket may still have open, and
// letting a second ticket start on the same system is the wrong risk to
// take for a temporary, author-attended label.
func TestResyncStillHoldsItsMutex(t *testing.T) {
	repairing := tk("T1", protocol.ReadyForRework, func(x *Ticket) {
		x.Labels = []string{LabelResync, "system:delivery"}
	})
	other := tk("T2", protocol.ReadyForDev, func(x *Ticket) { x.Labels = []string{"system:delivery"} })
	s := snap(repairing, other)
	if holder, _ := MutexHolder(s, other); holder == nil {
		t.Error("a ticket under repair released its system mutex")
	}
}

func TestPrecedenceOrdersDispatch(t *testing.T) {
	older := tk("T1", protocol.ReadyForDev, func(t *Ticket) { t.CreatedAt = t0.Add(-3 * time.Hour) })
	rework := tk("T2", protocol.ReadyForRework, func(t *Ticket) { t.CreatedAt = t0.Add(-time.Hour) })
	urgent := tk("T3", protocol.ReadyForDev, func(t *Ticket) {
		t.Priority = 1
		t.CreatedAt = t0.Add(-time.Minute)
	})
	acts := Sweep(snap(older, rework, urgent))
	if a := find(acts, ActDispatch, "T3"); a == nil || a.Agent != AgentDev {
		t.Fatalf("urgent must win pickup: %v", acts)
	}
	for _, id := range []string{"T1", "T2"} {
		if a := find(acts, ActDispatch, id); a != nil && a.Agent == AgentDev {
			t.Errorf("second dev dispatch to %s: dev agent is singular", id)
		}
	}

	// Without the urgent ticket, rework precedes first-pass dev work.
	acts = Sweep(snap(older, rework))
	if a := find(acts, ActDispatch, "T2"); a == nil {
		t.Errorf("rework should be picked before Ready for dev: %v", acts)
	}
}

func TestBoundaryCreationPauseAndDrain(t *testing.T) {
	done := tk("T1", protocol.Done)
	s := snap(done)
	a := find(Sweep(s), ActCreateBoundary, "")
	if a == nil || a.Milestone != "M1" {
		t.Fatalf("want boundary creation, got %v", Sweep(s))
	}

	// With the boundary open: only blockers and urgent tickets dispatch.
	boundary := tk("B1", protocol.Todo, func(t *Ticket) {
		t.Labels = []string{LabelBoundary}
		t.Title = "Milestone boundary — M1"
	})
	blocker := tk("T2", protocol.ReadyForDev, func(t *Ticket) { t.Blocks = []string{"B1"} })
	bystander := tk("T3", protocol.ReadyForDev, func(t *Ticket) { t.CreatedAt = t0.Add(-2 * time.Hour) })

	acts := Sweep(snap(done, boundary, blocker, bystander))
	if find(acts, ActCreateBoundary, "") != nil {
		t.Error("boundary created twice")
	}
	if a := find(acts, ActDispatch, "T2"); a == nil {
		t.Errorf("blocker must drain during pause: %v", acts)
	}
	if a := find(acts, ActDispatch, "T3"); a != nil {
		t.Errorf("bystander dispatched during pause: %v", *a)
	}

	// Urgent overrides the pause even without blocking the boundary.
	bystander.Priority = 1
	acts = Sweep(snap(done, boundary, bystander))
	if a := find(acts, ActDispatch, "T3"); a == nil {
		t.Errorf("urgent must override the pause: %v", acts)
	}
}

func TestBoundaryAgentDispatchedOnAuthorSignal(t *testing.T) {
	// Carrying a passing verdict, because the agent is gated on one: a
	// boundary re-entering In progress with the suite unproven re-runs
	// the suite first (DESIGN §10).
	boundary := tk("B1", protocol.InProgress, func(t *Ticket) {
		t.Labels = []string{LabelBoundary}
	}, withComment(marker.LiveSuite, map[string]string{"result": "pass"}),
		arrived(protocol.Todo, RoleAuthor))
	acts := Sweep(snap(boundary))
	if a := find(acts, ActDispatch, "B1"); a == nil || a.Agent != AgentBoundary {
		t.Errorf("want boundary agent dispatch, got %v", acts)
	}

	// Re-entry after a dead run resumes; a dead run without re-entry is a
	// stale claim instead.
	boundary.Run = &Run{ID: "r1", Kind: AgentBoundary, Live: false, EndedAt: t0.Add(-5 * time.Minute)}
	boundary.StateSince = t0.Add(-time.Minute)
	acts = Sweep(snap(boundary))
	if a := find(acts, ActDispatch, "B1"); a == nil {
		t.Errorf("re-entered boundary must re-dispatch: %v", acts)
	}

	boundary.StateSince = t0.Add(-time.Hour)
	boundary.Run.EndedAt = t0.Add(-45 * time.Minute)
	acts = Sweep(snap(boundary))
	if a := find(acts, ActDispatch, "B1"); a != nil {
		t.Errorf("dead run without re-entry must not re-dispatch: %v", *a)
	}
	if a := find(acts, ActTransition, "B1"); a == nil || a.To != protocol.Blocked {
		t.Errorf("want stale claim on boundary, got %v", acts)
	}
}

func TestBlockedByOpenTicketSkipsDispatch(t *testing.T) {
	dep := tk("T1", protocol.Designing)
	blocked := tk("T2", protocol.ReadyForDev, func(t *Ticket) { t.BlockedBy = []string{"T1"} })
	acts := Sweep(snap(dep, blocked))
	if a := find(acts, ActDispatch, "T2"); a != nil && a.Agent == AgentDev {
		t.Errorf("blocked ticket dispatched: %v", *a)
	}
}

func TestReEvaluateBlocksPickupAndTriggersReRead(t *testing.T) {
	flagged := tk("T1", protocol.ReadyForDev, func(t *Ticket) { t.Labels = []string{LabelReEvaluate} })
	acts := Sweep(snap(flagged))
	if a := find(acts, ActDispatch, "T1"); a == nil || a.Agent != AgentDesign {
		t.Errorf("want design re-read dispatch, got %v", acts)
	}
	for _, a := range acts {
		if a.Kind == ActDispatch && a.Agent == AgentDev {
			t.Errorf("flagged ticket picked up by dev: %v", a)
		}
	}
}

func TestLiveSuiteDispatchesWhenBoundaryOpens(t *testing.T) {
	b := tk("B1", protocol.Todo, func(t *Ticket) { t.Labels = []string{LabelBoundary} })
	acts := Sweep(snap(b))
	a := find(acts, ActDispatch, "B1")
	if a == nil || a.Agent != AgentLiveSuite {
		t.Fatalf("want a live-suite dispatch for the open boundary ticket, got %v", acts)
	}
}

func TestLiveSuiteNotRedispatchedAfterResult(t *testing.T) {
	b := tk("B1", protocol.Todo,
		func(t *Ticket) { t.Labels = []string{LabelBoundary} },
		withComment(marker.LiveSuite, map[string]string{"result": "fail", "run": "https://ci/1"}))
	if a := find(Sweep(snap(b)), ActDispatch, "B1"); a != nil {
		t.Fatalf("a verdict ends the first-run loop — no dispatch wanted, got %v", a)
	}
}

func TestLiveSuiteNotRedispatchedWhileRunningOrAfterDeadRun(t *testing.T) {
	live := tk("B1", protocol.Todo, func(t *Ticket) {
		t.Labels = []string{LabelBoundary}
		t.Run = &Run{ID: "r1", Kind: AgentLiveSuite, Live: true}
	})
	if a := find(Sweep(snap(live)), ActDispatch, "B1"); a != nil {
		t.Fatalf("live run: no second dispatch wanted, got %v", a)
	}
	// A run that died without posting its marker is the author's to
	// re-run — resurrecting it silently would hide the failure.
	dead := tk("B1", protocol.Todo, func(t *Ticket) {
		t.Labels = []string{LabelBoundary}
		t.StateSince = t0.Add(-time.Hour)
		t.Run = &Run{ID: "r1", Kind: AgentLiveSuite, Live: false, EndedAt: t0.Add(-time.Minute)}
	})
	if a := find(Sweep(snap(dead)), ActDispatch, "B1"); a != nil {
		t.Fatalf("dead run without a marker: no silent resurrection, got %v", a)
	}
}

// Entering In progress with the suite unproven re-runs it, and the
// boundary agent waits. The two dispatches are mutually exclusive by
// construction — one fires when the verdict is satisfied, the other when
// it is not — so the agent can never start against an unproven tree.
//
// This replaces a test asserting the live suite dispatches only from
// Todo. That was true and is the thing that changed: a suite that failed
// or never ran left the boundary agent free to run anyway, and the only
// record was a comment nobody was looking at.
func TestLiveSuiteRerunsWhenTheBoundaryReentersInProgress(t *testing.T) {
	for _, c := range []struct {
		name    string
		verdict string
		want    AgentKind
	}{
		{"never ran", "", AgentLiveSuite},
		{"failed", "fail", AgentLiveSuite},
		{"passed", "pass", AgentBoundary},
		// Neither verdict, and the author's call — not a barrier to it.
		{"no tests", "no-tests", AgentBoundary},
	} {
		muts := []func(*Ticket){func(t *Ticket) { t.Labels = []string{LabelBoundary} }}
		if c.verdict != "" {
			muts = append(muts, withComment(marker.LiveSuite, map[string]string{"result": c.verdict}))
		}
		// Already reported, so the block rule is not what is under test.
		if c.verdict == "fail" {
			muts = append(muts, withComment(marker.Blocked, map[string]string{"live-suite": "1"}))
		}
		muts = append(muts, arrived(protocol.Todo, RoleAuthor))
		b := tk("B1", protocol.InProgress, muts...)
		a := find(Sweep(snap(b)), ActDispatch, "B1")
		if a == nil || a.Agent != c.want {
			t.Errorf("%s: dispatched %v, want %s", c.name, a, c.want)
		}
	}
}

// Every arrival in Blocked says where it came from. Only the author
// moves a ticket out and they choose the state (DESIGN §12), so the
// origin is the answer to the question they are being asked — and it
// used to live only in the tracker's history, where no tool could read
// it and the author had to remember. Asserted over the real sweep paths
// rather than over the helper, so a rule that grew its own transition
// would be caught here.
func TestEveryBlockedArrivalRecordsWhereItCameFrom(t *testing.T) {
	cases := []struct {
		name string
		snap *Snapshot
	}{
		{"stale claim", snap(tk("T1", protocol.InProgress, func(t *Ticket) {
			t.StateSince = t0.Add(-time.Hour)
			t.Run = &Run{ID: "r1", Kind: AgentDev, Live: false, EndedAt: t0.Add(-30 * time.Minute)}
		}))},
		{"CI red twice", snap(tk("T1", protocol.Checks,
			withComment(marker.CIRed, map[string]string{"run": "https://ci/1", "attempt": "1"}),
			func(t *Ticket) { t.CI = CIInfo{Status: CIRed, RunURL: "https://ci/2"} }))},
		{"second reconcile bounce", snap(tk("T1", protocol.ReadyForRework,
			withComment(marker.ReconcileBounce, map[string]string{"n": "1"}),
			withComment(marker.ReconcileBounce, map[string]string{"n": "2"})))},
		{"deploy failed", snap(tk("T1", protocol.Merged, func(t *Ticket) {
			t.Deploy = DeployFailed
		}))},
		{"deployed with needs-review", snap(tk("T1", protocol.Merged, func(t *Ticket) {
			t.Deploy = DeployDeployed
			t.Labels = []string{LabelNeedsReview}
		}))},
		{"deploy timeout", snap(tk("T1", protocol.Merged, func(t *Ticket) {
			t.StateSince = t0.Add(-100 * time.Hour)
		}))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := c.snap.Tickets[0].State
			var blocked *Action
			for _, a := range Sweep(c.snap) {
				if a.Kind == ActTransition && a.To == protocol.Blocked {
					got := a
					blocked = &got
				}
			}
			if blocked == nil {
				t.Fatal("no transition into Blocked — the case no longer exercises what it names")
			}
			if blocked.Marker == nil {
				t.Fatal("blocked with no marker at all; the origin has nowhere to live")
			}
			if got := blocked.Marker.Fields["from"]; got != string(want) {
				t.Errorf("from = %q, want %q", got, want)
			}
		})
	}
}

// The test above can only check paths it knows about. This one catches
// the path nobody wrote a case for: block() is the single constructor
// for a transition into Blocked, so a new rule that builds its own
// Action reaches Blocked without an origin and without failing anything
// above.
func TestOnlyBlockConstructsATransitionIntoBlocked(t *testing.T) {
	body, err := os.ReadFile("sweep.go")
	if err != nil {
		t.Fatal(err)
	}
	var lines []int
	for i, line := range strings.Split(string(body), "\n") {
		if strings.Contains(line, "To:") && strings.Contains(line, "protocol.Blocked") {
			lines = append(lines, i+1)
		}
	}
	if len(lines) != 1 {
		t.Errorf("sweep.go builds a Blocked transition at lines %v; block() must be the only one, or the origin is optional again", lines)
	}
}

// The failure comment is the rework scope (DESIGN §2.3), and DESIGN §12
// has always described it as naming the failing jobs and linking the
// run. For a long time it only linked — and the link is precisely the
// half the dev agent cannot follow.
func TestCIRedCommentNamesTheFailingJobs(t *testing.T) {
	red := tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIRed, RunURL: "https://ci/9", FailedJobs: []string{"gates", "mutex-audit"}}
	})
	a := find(Sweep(snap(red)), ActTransition, "T1")
	if a == nil || a.To != protocol.ReadyForRework {
		t.Fatalf("want a bounce to rework, got %v", a)
	}
	for _, want := range []string{"gates", "mutex-audit"} {
		if !strings.Contains(a.Prose, want) {
			t.Errorf("the comment does not name %q, so the scope says only \"look at the link\":\n%s", want, a.Prose)
		}
	}
	if got := a.Marker.Fields["jobs"]; got != "gates,mutex-audit" {
		t.Errorf("marker jobs = %q, want the failing set — a later pass reads markers, not prose", got)
	}
}

// Three conflicts is a sequencing problem, not a stale branch. Without a
// bound, an active main and a slow ticket loop between Reconciling and
// the queue forever, burning a full agent run each pass. Looser than the
// reconcile-bounce rule at two, because that one counts findings about
// the work and this one counts other people's merges.
func TestThirdMergeConflictBlocks(t *testing.T) {
	conflict := func(n string) func(*Ticket) {
		return withComment(marker.MergeConflict, map[string]string{"pr": "5", "attempt": n})
	}
	two := snap(tk("T1", protocol.ReadyForRework, conflict("1"), conflict("2")))
	if a := find(Sweep(two), ActTransition, "T1"); a != nil && a.To == protocol.Blocked {
		t.Error("blocked on the second conflict; main moving twice while one ticket lands is ordinary")
	}

	three := snap(tk("T1", protocol.ReadyForRework, conflict("1"), conflict("2"), conflict("3")))
	a := find(Sweep(three), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want Blocked on the third conflict, got %v", a)
	}
	if got := a.Marker.Fields["from"]; got != string(protocol.ReadyForRework) {
		t.Errorf("blocked marker from = %q, want ready_for_rework", got)
	}
	if !strings.Contains(a.Prose, "sequencing") {
		t.Errorf("the comment blames the ticket rather than the ordering:\n%s", a.Prose)
	}
	// The escalation is not itself a conflict, and posting it under the
	// kind being counted is what let one escalation take the ticket from
	// three conflicts to four.
	if a.Marker.Kind != marker.Blocked {
		t.Errorf("escalation posted a %q marker; it inflates the count it reads", a.Marker.Kind)
	}
	if a.Marker.Fields["conflicts"] != "3" {
		t.Errorf("want the escalation to record conflicts=3, got %v", a.Marker)
	}
}

// The same rule as the bounce escalation, for the same reason: the author
// chooses the state out of Blocked (DESIGN §12), and another attempt is a
// legitimate choice when the fix is to land ahead of the competing work.
func TestTheAuthorsReturnAfterAConflictBlockIsNotReEscalated(t *testing.T) {
	conflict := func(n string) func(*Ticket) {
		return withComment(marker.MergeConflict, map[string]string{"pr": "5", "attempt": n})
	}
	s := snap(tk("T1", protocol.ReadyForRework, conflict("1"), conflict("2"), conflict("3"),
		withComment(marker.Blocked, map[string]string{"from": "ready_for_rework", "conflicts": "3"})))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil && a.To == protocol.Blocked {
		t.Errorf("re-blocked the author's return with no new conflict: %v", *a)
	}
	if a := find(Sweep(s), ActDispatch, "T1"); a == nil || a.Agent != AgentDev {
		t.Errorf("no dev run dispatched for the returned ticket: %v", Sweep(s))
	}
}

// A fourth conflict is new information: main moved again, and the author
// is owed the same stop the third gave them.
func TestAFourthConflictEscalatesAgain(t *testing.T) {
	conflict := func(n string) func(*Ticket) {
		return withComment(marker.MergeConflict, map[string]string{"pr": "5", "attempt": n})
	}
	s := snap(tk("T1", protocol.ReadyForRework, conflict("1"), conflict("2"), conflict("3"),
		withComment(marker.Blocked, map[string]string{"from": "ready_for_rework", "conflicts": "3"}),
		conflict("4")))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want a fourth conflict -> Blocked, got %v", a)
	}
	if a.Marker.Fields["conflicts"] != "4" {
		t.Errorf("want the escalation to record conflicts=4, got %v", a.Marker)
	}
}

// The count is conflict events, so one escalation must not advance it.
// Measured before the fix: three conflicts plus the escalation's own
// merge-conflict marker read as four, which is how the rule re-fired on
// its own output.
func TestTheConflictEscalationDoesNotInflateItsOwnCount(t *testing.T) {
	conflict := func(n string) func(*Ticket) {
		return withComment(marker.MergeConflict, map[string]string{"pr": "5", "attempt": n})
	}
	tick := tk("T1", protocol.ReadyForRework, conflict("1"), conflict("2"), conflict("3"))
	a := find(Sweep(snap(tick)), ActTransition, "T1")
	if a == nil || a.Marker == nil {
		t.Fatalf("no escalation to check: %v", a)
	}
	tick.Comments = append(tick.Comments, Comment{Body: a.Marker.Format(), Actor: RoleControlPlane, At: t0})
	if n := len(markersOf(tick, marker.MergeConflict)); n != 3 {
		t.Errorf("the escalation moved the conflict count to %d; it counts other people's merges, and it did not merge anything", n)
	}
}

// The hole this closes. A conflicted PR gets no CI run at all — GitHub
// builds no merge commit for one, so the `pull_request` event never
// fires — and Checks has no agent, so no stale-claim timeout applies.
// Before this, such a ticket had no exit whatsoever: not blocked, not
// timing out, just parked. The bounce that existed lived in reconcile,
// downstream of the verdict that was never coming.
func TestAConflictedBranchWithNoVerdictGoesToRework(t *testing.T) {
	s := snap(tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Mergeable: MergeConflicted, PRNumber: 20}
	}))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.ReadyForRework {
		t.Fatalf("want Ready for rework, got %v", a)
	}
	if a.Marker == nil || a.Marker.Kind != marker.MergeConflict {
		t.Fatalf("want a merge-conflict marker so the escalation counts it, got %v", a.Marker)
	}
	if a.Marker.Fields["pr"] != "20" || a.Marker.Fields["at"] != "checks" {
		t.Errorf("the marker does not say which PR or where it was seen: %v", a.Marker.Fields)
	}
	// The newest comment is the scope (DESIGN §2.3). A dev agent handed
	// a bounce reads it as a finding about the diff unless told
	// otherwise, and would re-litigate a design nothing has questioned.
	if !strings.Contains(a.Prose, "Nothing about the work is in question") {
		t.Errorf("the scope does not exempt the work:\n%s", a.Prose)
	}
}

// Unknown is GitHub saying "not computed yet", which it says about every
// freshly pushed branch. Reading it as a conflict would bounce healthy
// tickets out of Checks the moment they arrived.
func TestUnknownMergeabilityIsNotAConflict(t *testing.T) {
	s := snap(tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIPending, Mergeable: MergeUnknown}
	}))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil {
		t.Errorf("an uncomputed merge state moved the ticket: %v", *a)
	}
}

// A green PR that conflicts does not get promoted, even though
// reconcile's own bounce would have caught it. Promotion would spend a
// full model-driven reconcile pass on a merge that cannot land, and end
// in the same place. Reconcile's bounce stays for the case this cannot
// see: main moving between the snapshot and the merge attempt.
func TestAConflictOnAGreenPRBouncesWithoutSpendingAReconcilePass(t *testing.T) {
	s := snap(tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIGreen, RunURL: "https://ci/1", Mergeable: MergeConflicted}
	}))
	acts := Sweep(s)
	a := find(acts, ActTransition, "T1")
	if a == nil || a.To != protocol.ReadyForRework {
		t.Fatalf("want Ready for rework, got %v", a)
	}
	if d := find(acts, ActDispatch, "T1"); d != nil && d.Agent == AgentReconcile {
		t.Errorf("a reconcile pass was dispatched onto a branch that cannot merge: %v", *d)
	}
}

// Red CI is a report that happened, naming failing jobs, and that is the
// more specific scope. It lands in the same state, and merging main is
// part of the rework either way.
func TestRedCIKeepsItsOwnScopeEvenWhenTheBranchConflicts(t *testing.T) {
	s := snap(tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIRed, RunURL: "https://ci/9", Mergeable: MergeConflicted}
	}))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.ReadyForRework {
		t.Fatalf("want Ready for rework, got %v", a)
	}
	if a.Marker == nil || a.Marker.Kind != marker.CIRed {
		t.Errorf("the conflict swallowed the CI failure: %v", a.Marker)
	}
}

// One ticket, two detection points, one counter. Two conflicts already
// recorded by reconcile plus this one is the third, and the third is a
// sequencing problem rather than a stale branch — separate counters
// would each stop at two and it would never escalate.
func TestConflictsCountAcrossBothDetectionPoints(t *testing.T) {
	s := snap(tk("T1", protocol.Checks,
		withComment(marker.MergeConflict, map[string]string{"pr": "20", "attempt": "1"}),
		withComment(marker.MergeConflict, map[string]string{"pr": "20", "attempt": "2"}),
		func(t *Ticket) { t.CI = CIInfo{Mergeable: MergeConflicted, PRNumber: 20} }))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.Marker.Fields["attempt"] != "3" {
		t.Fatalf("want attempt 3 continuing reconcile's count, got %v", a)
	}
	// It still goes to the queue; the escalation fires on the next sweep,
	// from Ready for rework, where that rule lives.
	if a.To != protocol.ReadyForRework {
		t.Errorf("want Ready for rework, got %v", a.To)
	}
}

// The run correlated to a ticket is the newest of any kind; the kind
// expected comes from the state. When a dispatch is rejected outright —
// an invalid workflow file, a missing secret — no run of the expected
// kind ever exists, and the ticket used to report a dead run of that
// kind naming a *different* agent's run id. That sentence sent a real
// debugging session at a merge conflict for an hour while the actual
// fault was a duplicate YAML key in the reconcile workflow.
func TestAStaleClaimSaysSoWhenTheAgentWasNeverDispatched(t *testing.T) {
	s := snap(tk("T1", protocol.Reconciling, func(t *Ticket) {
		t.StateSince = t0.Add(-time.Hour)
		t.Run = &Run{ID: "31912796533", Kind: AgentDev, Live: false, EndedAt: t0.Add(-2 * time.Hour)}
	}))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want Blocked, got %v", a)
	}
	if strings.Contains(a.Prose, "The reconcile run claiming this ticket") {
		t.Errorf("a dev run is described as the reconcile run:\n%s", a.Prose)
	}
	for _, want := range []string{"No reconcile run was ever dispatched", "dev run `31912796533`", "workflow file"} {
		if !strings.Contains(a.Prose, want) {
			t.Errorf("the comment is missing %q:\n%s", want, a.Prose)
		}
	}
	// The marker carries what was actually found, so the history is
	// readable without re-deriving it from prose.
	if a.Marker == nil || a.Marker.Fields["dispatched"] != string(AgentDev) {
		t.Errorf("the marker does not record the run's real kind: %v", a.Marker)
	}
}

// The ordinary case is unchanged: the run is the right kind and it died.
func TestAStaleClaimOnTheRightAgentReadsAsBefore(t *testing.T) {
	s := snap(tk("T1", protocol.Reconciling, func(t *Ticket) {
		t.StateSince = t0.Add(-time.Hour)
		t.Run = &Run{ID: "99", Kind: AgentReconcile, Live: false, EndedAt: t0.Add(-2 * time.Hour)}
	}))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want Blocked, got %v", a)
	}
	if !strings.Contains(a.Prose, "The reconcile run claiming this ticket is no longer live") {
		t.Errorf("the ordinary stale-claim wording changed:\n%s", a.Prose)
	}
	if a.Marker.Fields["dispatched"] != "" {
		t.Error("a matching run recorded a mismatch")
	}
}

// The ORC-16 loop, in one test. A merged ticket whose deploy has landed
// holds mutex labels a queued ticket needs. Before the fold, the pass
// reasoned about the world as it was at the top of the sweep: ORC-21 was
// still Merged for every rule, so the queue ticket was held — and on the
// beats where it was dispatched anyway, the pickup assertion refused it,
// at a full billed job each time.
func TestADeployedTicketReleasesItsMutexWithinTheSamePass(t *testing.T) {
	done := tk("T1", protocol.Merged, func(t *Ticket) {
		t.Deploy = DeployDeployed
		t.Labels = []string{"system:substrate"}
	})
	queued := tk("T2", protocol.ReadyForDev, func(t *Ticket) {
		t.Labels = []string{"system:substrate"}
	})

	acts := Sweep(snap(done, queued))
	if a := find(acts, ActTransition, "T1"); a == nil || a.To != protocol.Done {
		t.Fatalf("the deployed ticket did not retire: %v", a)
	}
	d := find(acts, ActDispatch, "T2")
	if d == nil || d.Agent != AgentDev {
		t.Errorf("the queued ticket was not dispatched once its label was free: %v", acts)
	}
}

// The other half: while the holder is genuinely in flight, the sweep must
// not dispatch into an assertion that can only refuse. Every one of those
// cost a job with a checkout, a toolchain and a service container.
func TestAHeldMutexStopsTheDispatchRatherThanThePickup(t *testing.T) {
	holder := tk("T1", protocol.Checks, func(t *Ticket) {
		t.Labels = []string{"system:substrate"}
	})
	queued := tk("T2", protocol.ReadyForDev, func(t *Ticket) {
		t.Labels = []string{"system:substrate"}
	})

	s := snap(holder, queued)
	if d := find(Sweep(s), ActDispatch, "T2"); d != nil {
		t.Errorf("dispatched into a pickup that refuses: %v", *d)
	}
	// And the two agree, which is the reason they share a function.
	if err := VerifyPickup(s, queued.ID, AgentDev, "r9"); err == nil {
		t.Error("the dispatcher declined but the pickup assertion would have allowed it")
	}
}

// Merged is finished work: the branch is gone and its commits are on
// main, so a ticket starting afterwards contains it rather than racing
// it. Holding the labels through Merged meant holding them for the whole
// deploy-detection window — on a platform with no deploy webhook, up to
// an hour of a queue held by a ticket that was done.
func TestMergedDoesNotHoldTheMutex(t *testing.T) {
	merged := tk("T1", protocol.Merged, func(t *Ticket) { t.Labels = []string{"screen:home"} })
	queued := tk("T2", protocol.ReadyForDev, func(t *Ticket) { t.Labels = []string{"screen:home"} })
	if other, _ := MutexHolder(snap(merged, queued), queued); other != nil {
		t.Errorf("a merged ticket still holds %q", "screen:home")
	}
	// Every earlier state still does.
	for _, st := range []protocol.State{
		protocol.ReadyForDev, protocol.InProgress, protocol.Checks,
		protocol.Reconciling, protocol.ReadyForRework, protocol.Reworking,
	} {
		holder := tk("T3", st, func(t *Ticket) { t.Labels = []string{"screen:home"} })
		if other, _ := MutexHolder(snap(holder, queued), queued); other == nil {
			t.Errorf("%s does not hold the mutex", st)
		}
	}
}

// The fold must not leak. A caller that sweeps the same snapshot twice
// has to get the same answer — that property is what Ring 2's
// convergence loop and every table test rest on.
func TestSweepDoesNotMutateTheCallersSnapshot(t *testing.T) {
	s := snap(
		tk("T1", protocol.Merged, func(t *Ticket) { t.Deploy = DeployDeployed }),
		tk("T2", protocol.Checks, func(t *Ticket) { t.CI = CIInfo{Status: CIGreen, RunURL: "https://ci/1"} }),
	)
	first := Sweep(s)
	for _, tk := range s.Tickets {
		if tk.Key == "T1" && tk.State != protocol.Merged {
			t.Fatalf("the caller's snapshot was written: T1 is %s", tk.State)
		}
	}
	second := Sweep(s)
	if len(first) != len(second) {
		t.Fatalf("sweeping twice gave %d then %d actions", len(first), len(second))
	}
	for i := range first {
		if first[i].String() != second[i].String() {
			t.Errorf("action %d differs between passes:\n%s\n%s", i, first[i], second[i])
		}
	}
}

// One transition per ticket per pass. The fold makes a ticket's new state
// visible to later rules, and without this guard those rules would act on
// it — a ticket promoted into Reconciling being judged, in the same pass,
// as a Reconciling ticket whose claim is stale.
func TestAFoldedTicketIsNotActedOnTwice(t *testing.T) {
	s := snap(tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIGreen, RunURL: "https://ci/1"}
		t.StateSince = t0.Add(-time.Hour)
		t.Run = &Run{ID: "r1", Kind: AgentDev, Live: false, EndedAt: t0.Add(-time.Hour)}
	}))
	var transitions int
	for _, a := range Sweep(s) {
		if a.Kind == ActTransition && a.TicketID == "T1" {
			transitions++
		}
	}
	if transitions != 1 {
		t.Errorf("got %d transitions for one ticket in one pass, want 1", transitions)
	}
}

// The bug this whole mechanism exists for. On a solo workspace the
// harness holds the author's Linear key, so a role read off the tracker
// identity answered "control plane" for the human's moves too — and that
// is the one role the revert rules trust. Every §9 invariant was off.
// The record answers from what the pipeline did, not from who it looked
// like.
func TestAHumanMoveIsJudgedEvenWhenItWearsThePipelinesIdentity(t *testing.T) {
	victim := tk("T1", protocol.InProgress)
	s := snap(victim)
	// The pipeline left it in the queue; the tracker says In progress.
	s.Recorded["T1"] = RecordedMove{From: protocol.DesignReview, To: protocol.ReadyForDev, Role: RoleControlPlane}

	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.ReadyForDev || a.Marker.Fields["rule"] != "claim" {
		t.Fatalf("a hand-moved claim was not reverted: %v", a)
	}
}

// And the converse, which is the failure mode of getting this wrong in
// the other direction: the pipeline's own moves must not be reverted.
func TestThePipelinesOwnMoveIsNotJudgedAsAHumans(t *testing.T) {
	moved := tk("T1", protocol.InProgress)
	s := snap(moved)
	s.Recorded["T1"] = RecordedMove{From: protocol.ReadyForDev, To: protocol.InProgress, Role: RoleDev}
	if a := find(Sweep(s), ActTransition, "T1"); a != nil {
		t.Errorf("the dev agent's own claim was reverted: %v", *a)
	}
}

// An agent's move is recorded, and being recorded is not being excused.
// A design pass promoting straight past Design review is precisely what
// §9 exists to catch, and it is the pipeline doing it.
func TestARecordedAgentMoveIsStillJudged(t *testing.T) {
	skipped := tk("T1", protocol.ReadyForDev)
	s := snap(skipped)
	s.Recorded["T1"] = RecordedMove{From: protocol.Designing, To: protocol.ReadyForDev, Role: RoleDesign}
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.Marker.Fields["rule"] != "sign-off" {
		t.Errorf("a design pass skipping review was not caught: %v", a)
	}
}

// Every ticket that existed before the store did has no record. Reading
// that as "a human did it" would revert the entire backlog on the first
// sweep after deploy, which is the one migration failure that cannot be
// undone by waiting.
func TestATicketWithNoRecordIsNotJudged(t *testing.T) {
	s := snap(tk("T1", protocol.InProgress))
	delete(s.Recorded, "T1")
	for _, a := range Sweep(s) {
		if a.Kind == ActTransition && a.TicketID == "T1" && strings.HasPrefix(a.Reason, "invariant") {
			t.Errorf("an unrecorded ticket was judged: %v", a)
		}
	}
}

// The dispatcher's singularity guard reads Run.Live, and that marker is
// posted by the claim — inside the dispatched job, after boot, checkout,
// toolchain and deps. About ninety seconds on catapult, and any sweep
// landing in that window sees an idle agent and dispatches again. Two
// boundary agents ran one ticket to completion that way: two scans,
// twenty-two minutes of spend, four tickets for two findings.
func TestPickupRefusesWhenAnAgentOfThatKindIsAlreadyRunning(t *testing.T) {
	running := tk("T1", protocol.InProgress, func(t *Ticket) {
		t.Labels = []string{LabelBoundary}
		t.Run = &Run{ID: "31959743407", Kind: AgentBoundary, Live: true}
	})
	second := tk("T2", protocol.InProgress, func(t *Ticket) { t.Labels = []string{LabelBoundary} })

	err := VerifyPickup(snap(running, second), "T2", AgentBoundary, "r2")
	if err == nil {
		t.Fatal("a second boundary agent was allowed to claim")
	}
	if !strings.Contains(err.Error(), "31959743407") {
		t.Errorf("the refusal does not name the run already holding it: %v", err)
	}
}

// A ticket's own live run is not a second agent. A claim re-entering
// after a resume is the same run, and refusing it would break the
// documented recovery path.
func TestPickupAllowsATicketToClaimAgainstItsOwnRun(t *testing.T) {
	resuming := tk("T1", protocol.InProgress, func(t *Ticket) {
		t.Labels = []string{LabelBoundary}
		t.Run = &Run{ID: "r1", Kind: AgentBoundary, Live: true}
		t.LiveRuns = []Run{{ID: "r1", Kind: AgentBoundary, Live: true}}
	})
	if err := VerifyPickup(snap(resuming), "T1", AgentBoundary, "r1"); err != nil {
		t.Errorf("a resume was refused as a duplicate: %v", err)
	}
}

// The case the check above could not see. Two runs on ONE ticket each
// skipped "the ticket's own run" as their own, so neither refused —
// ORC-45 was dispatched twice 82 seconds apart and both scans ran to
// completion, filing a duplicated set of tickets for about twenty-two
// minutes of duplicate model spend.
//
// Comparing run ids rather than tickets separates the two cases the
// single collapsed Run cannot: a resume carries the id it was
// dispatched under, a second agent does not.
func TestPickupRefusesASecondRunOnTheSameTicket(t *testing.T) {
	contested := tk("T1", protocol.InProgress, func(t *Ticket) {
		t.Labels = []string{LabelBoundary}
		// Run collapses to the newest live one — which, for the second
		// agent, is itself. That is why this cannot be read off Run.
		t.Run = &Run{ID: "31959809052", Kind: AgentBoundary, Live: true}
		t.LiveRuns = []Run{
			{ID: "31959809052", Kind: AgentBoundary, Live: true},
			{ID: "31959743407", Kind: AgentBoundary, Live: true},
		}
	})
	err := VerifyPickup(snap(contested), "T1", AgentBoundary, "31959809052")
	if err == nil {
		t.Fatal("the second run on one ticket was allowed to claim")
	}
	if !strings.Contains(err.Error(), "31959743407") {
		t.Errorf("the refusal does not name the run already live: %v", err)
	}

	// A run of another kind on the same ticket is a different question,
	// and not this one's to answer.
	contested.LiveRuns[1].Kind = AgentDesign
	if err := VerifyPickup(snap(contested), "T1", AgentBoundary, "31959809052"); err != nil {
		t.Errorf("refused over a run of a different kind: %v", err)
	}
}

// A harness that cannot tell the claim its own run id gets the old
// behaviour rather than a refusal it can never satisfy: every run would
// look like somebody else's, and no boundary would ever claim.
func TestPickupWithoutARunIDDoesNotRefuseItself(t *testing.T) {
	contested := tk("T1", protocol.InProgress, func(t *Ticket) {
		t.Labels = []string{LabelBoundary}
		t.Run = &Run{ID: "r1", Kind: AgentBoundary, Live: true}
		t.LiveRuns = []Run{{ID: "r1", Kind: AgentBoundary, Live: true}}
	})
	if err := VerifyPickup(snap(contested), "T1", AgentBoundary, ""); err != nil {
		t.Errorf("a claim with no run id was refused: %v", err)
	}
}

// A record with no origin stopped the whole sweep. The writer matrix
// judges edges, and a revert sends the ticket back to the edge's origin
// — so a half-edge planned `transition <id> -> ""` and Execute died on
// "no tracker state for \"\"". One unjudgeable ticket froze every other
// ticket in the project.
func TestAnOriginlessRecordIsNotJudged(t *testing.T) {
	for _, rec := range []RecordedMove{
		{To: protocol.ReadyForDev, Role: RoleDesign},         // finish with no snapshot behind it
		{To: protocol.ReadyForDev, Role: RoleControlPlane},   // as ingest writes them
		{From: protocol.Designing, To: "", Role: RoleDesign}, // a destination nobody recorded
	} {
		s := snap(tk("T1", protocol.ReadyForDev))
		s.Recorded["T1"] = rec
		for _, a := range Sweep(s) {
			if a.Kind != ActTransition || a.TicketID != "T1" {
				continue
			}
			if a.To == "" {
				t.Errorf("planned a transition to nowhere from %+v", rec)
			}
			if strings.HasPrefix(a.Reason, "invariant") {
				t.Errorf("judged a half-edge %+v: %s", rec, a.Reason)
			}
		}
	}
}

// And a complete record is still judged, so the guard above did not
// quietly turn the invariants off again.
func TestACompleteRecordIsStillJudged(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDev))
	s.Recorded["T1"] = RecordedMove{From: protocol.Designing, To: protocol.ReadyForDev, Role: RoleDesign}
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.Marker.Fields["rule"] != "sign-off" {
		t.Fatalf("a complete edge went unjudged: %v", a)
	}
	if a.To != protocol.Designing {
		t.Errorf("revert target = %q, want the recorded origin", a.To)
	}
}

// Some work is legal for nobody but the author. The gate set lives in
// pipeline.config.json and ci.yml, both author-only (DESIGN §5), so a
// ticket scoped to change it used to be claimed by the dev agent, which
// then found every file it needed closed to it and handed back — a full
// run, checkout and toolchain and model, spent to be told no, and
// repeated on every beat because the refusal leaves the ticket in the
// queue.
//
// The label routes it to the human instead: no design dispatch, no dev
// dispatch, in any state.
func TestAuthorOnlyTicketsAreNeverDispatched(t *testing.T) {
	for _, state := range []protocol.State{protocol.Designing, protocol.ReadyForDev, protocol.ReadyForRework} {
		t.Run(string(state), func(t *testing.T) {
			mine := tk("A1", state, func(t *Ticket) { t.Labels = []string{LabelAuthorOnly} })
			s := snap(mine)
			for _, a := range Sweep(s) {
				if a.Kind == ActDispatch && a.TicketID == mine.ID {
					t.Errorf("dispatched %s for an author-only ticket in %s", a.Agent, state)
				}
			}
		})
	}

	// And the label is not a general freeze: an ordinary ticket beside it
	// still goes out, so one author-only ticket at the head of the queue
	// cannot stall everything behind it.
	mine := tk("A1", protocol.ReadyForDev, func(t *Ticket) { t.Labels = []string{LabelAuthorOnly} })
	other := tk("B1", protocol.ReadyForDev)
	var dispatched bool
	for _, a := range Sweep(snap(mine, other)) {
		if a.Kind == ActDispatch && a.Agent == AgentDev && a.TicketID == other.ID {
			dispatched = true
		}
	}
	if !dispatched {
		t.Error("an author-only ticket at the head of the queue blocked the ticket behind it")
	}
}

// The rest of "the pipeline ignores it": Todo to Done is the author's
// workflow for these, and the §9 writer matrix has no row for it. Only
// the post-deploy check writes Done, so the author closing an
// author-only ticket read as a done-writer violation and got reverted —
// and since the revert is itself an arrival the author then moved again,
// every sweep, forever.
func TestAuthorOnlyClosuresAreNotReverted(t *testing.T) {
	mine := tk("A1", protocol.Done, func(t *Ticket) {
		t.Labels = []string{LabelAuthorOnly}
	}, arrived(protocol.Todo, RoleAuthor))

	for _, a := range Sweep(snap(mine)) {
		if a.Kind == ActTransition && a.TicketID == mine.ID {
			t.Errorf("reverted an author-only closure: %+v", a)
		}
	}

	// The exemption is the label, not the states: the same move on an
	// ordinary ticket is still a violation.
	ordinary := tk("B1", protocol.Done, arrived(protocol.Todo, RoleAuthor))
	if a := find(Sweep(snap(ordinary)), ActTransition, "B1"); a == nil {
		t.Error("an ordinary Todo to Done went unjudged")
	}
}

// Author-only work never reaches an agent, so nothing in the pipeline
// ever observes it finishing. Counting it in the mutex would park every
// ticket sharing its screen or system behind a human's calendar.
func TestAuthorOnlyDoesNotHoldTheMutex(t *testing.T) {
	mine := tk("A1", protocol.InProgress, func(t *Ticket) {
		t.Labels = []string{"screen:home", LabelAuthorOnly}
	})
	queued := tk("T2", protocol.ReadyForDev, func(t *Ticket) { t.Labels = []string{"screen:home"} })

	s := snap(mine, queued)
	if other, l := MutexHolder(s, queued); other != nil {
		t.Errorf("an author-only ticket holds %q", l)
	}
	if d := find(Sweep(s), ActDispatch, "T2"); d == nil {
		t.Error("the queued ticket was held behind an author-only ticket")
	}
}

// The gates are the pipeline's, and author-only work bypasses them
// (DESIGN §8). A ticket the author parked in Checks would otherwise
// spend a reconcile pass judging a diff no agent wrote against a scope
// no agent was given.
func TestAuthorOnlyDoesNotDispatchReconcile(t *testing.T) {
	mine := tk("A1", protocol.Checks, func(t *Ticket) {
		t.Labels = []string{LabelAuthorOnly}
		t.CI = CIInfo{Status: CIGreen, RunURL: "https://ci/1"}
	})
	for _, a := range Sweep(snap(mine)) {
		if a.TicketID == mine.ID {
			t.Errorf("the sweep acted on an author-only ticket in Checks: %+v", a)
		}
	}
}

// And the agent-side half, for a run that arrives some other way — a
// hand-fired workflow, or a dispatch planned in the beat before the
// label went on.
func TestAuthorOnlyPickupIsRefused(t *testing.T) {
	for _, k := range []struct {
		kind  AgentKind
		state protocol.State
	}{
		{AgentDev, protocol.ReadyForDev},
		{AgentDesign, protocol.Designing},
		{AgentReconcile, protocol.Reconciling},
	} {
		mine := tk("A1", k.state, func(t *Ticket) { t.Labels = []string{LabelAuthorOnly} })
		err := VerifyPickup(snap(mine), mine.ID, k.kind, "r1")
		if err == nil {
			t.Errorf("%s claimed an author-only ticket", k.kind)
			continue
		}
		if !strings.Contains(err.Error(), LabelAuthorOnly) {
			t.Errorf("%s refusal does not name the label: %v", k.kind, err)
		}
	}
}

// The dev agent's singularity is inferred from state as well as from live
// runs, because a dead run in a dev state must not be double-dispatched.
// That inference only holds for tickets a dev run could have moved: an
// author-only ticket in In progress means a human is working on it, and
// reading it as "the dev agent is busy" stops the whole queue.
func TestAuthorOnlyInADevStateDoesNotFreezeTheQueue(t *testing.T) {
	for _, st := range []protocol.State{protocol.InProgress, protocol.Reworking} {
		mine := tk("A1", st, func(t *Ticket) { t.Labels = []string{LabelAuthorOnly} })
		queued := tk("T2", protocol.ReadyForDev)
		if d := find(Sweep(snap(mine, queued)), ActDispatch, "T2"); d == nil {
			t.Errorf("an author-only ticket in %s held the dev agent", st)
		}
	}
}

// Every refusal carries the sentinel, and nothing else does. The
// distinction is what lets a workflow park a ticket on a harness
// failure without parking one the protocol deliberately left alone —
// so a refusal that forgot to carry it would be filed as a broken
// pipeline, and a genuine failure that acquired it would be filed as
// the pipeline working and told nobody.
func TestEveryPickupRefusalIsMarkedAsOne(t *testing.T) {
	cases := []struct {
		name string
		snap *Snapshot
		id   string
		kind AgentKind
	}{
		{"kill switch", func() *Snapshot {
			s := snap(tk("T1", protocol.ReadyForDev))
			s.KillSwitch = true
			return s
		}(), "T1", AgentDev},
		{"author-only", snap(tk("T1", protocol.ReadyForDev, func(t *Ticket) {
			t.Labels = []string{LabelAuthorOnly}
		})), "T1", AgentDev},
		{"wrong state", snap(tk("T1", protocol.Todo)), "T1", AgentDev},
		{"re-evaluate", snap(tk("T1", protocol.ReadyForDev, func(t *Ticket) {
			t.Labels = []string{LabelReEvaluate}
		})), "T1", AgentDev},
		{"mutex held", snap(
			tk("T1", protocol.Checks, func(t *Ticket) { t.Labels = []string{"system:core"} }),
			tk("T2", protocol.ReadyForDev, func(t *Ticket) { t.Labels = []string{"system:core"} }),
		), "T2", AgentDev},
		{"unknown ticket", snap(tk("T1", protocol.ReadyForDev)), "nope", AgentDev},
		{"unknown kind", snap(tk("T1", protocol.ReadyForDev)), "T1", AgentKind("wat")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyPickup(c.snap, c.id, c.kind, "r1")
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !Refused(err) {
				t.Errorf("refusal is not marked as one, so a workflow would park the ticket: %v", err)
			}
			// The sentinel must not swallow the reason a human reads.
			if len(err.Error()) < 20 {
				t.Errorf("refusal lost its message: %q", err)
			}
		})
	}

	// And the negative: a snapshot the harness could not build is not a
	// refusal, however it reaches the caller.
	if Refused(errors.New("github: HTTP 403: Resource not accessible")) {
		t.Error("a harness failure reads as a refusal, so nobody would be told about it")
	}
}

// The whole point of the design queue: dispatch reads it, and the claim
// is what moves the ticket out. Designing used to be both, so a run that
// died before claiming left a ticket asserting an agent was working on
// it — ORC-7 sat that way for 23 minutes.
func TestDesignDispatchesFromTheQueueAndNotFromDesigning(t *testing.T) {
	queued := tk("T1", protocol.ReadyForDesign)
	if d := find(Sweep(snap(queued)), ActDispatch, "T1"); d == nil || d.Agent != AgentDesign {
		t.Errorf("a ticket in the design queue was not dispatched: %v", d)
	}

	// A ticket already in Designing has been claimed. Dispatching again
	// would be a second agent on somebody else's work, and it is the
	// stale-claim rule's business if the first one died.
	claimed := tk("T2", protocol.Designing, func(t *Ticket) {
		t.Run = &Run{ID: "r1", Kind: AgentDesign, Live: true}
	})
	if d := find(Sweep(snap(claimed)), ActDispatch, "T2"); d != nil {
		t.Errorf("dispatched against a claimed ticket: %+v", *d)
	}
	// Including when its run is dead: that is a stale claim, not a queue.
	dead := tk("T3", protocol.Designing, func(t *Ticket) {
		t.Run = &Run{ID: "r1", Kind: AgentDesign, Live: false, EndedAt: t0.Add(-time.Hour)}
	})
	if d := find(Sweep(snap(dead)), ActDispatch, "T3"); d != nil {
		t.Errorf("resurrected a dead claim as a fresh dispatch: %+v", *d)
	}
}

// Designing is the design agent's claim, so a hand-move into it is the
// same violation a hand-move into In progress is. This rule could not
// exist while the author moved tickets into Designing to queue them.
func TestDesigningIsWrittenOnlyByTheDesignClaim(t *testing.T) {
	handMoved := tk("T1", protocol.Designing, arrived(protocol.Todo, RoleAuthor))
	a := find(Sweep(snap(handMoved)), ActTransition, "T1")
	if a == nil || a.Marker.Fields["rule"] != "claim" {
		t.Fatalf("a hand-moved Designing was not reverted: %v", a)
	}
	if a.To != protocol.Todo {
		t.Errorf("revert target = %q, want the recorded origin", a.To)
	}

	// The claim itself is fine.
	claimed := tk("T2", protocol.Designing, arrived(protocol.ReadyForDesign, RoleDesign))
	if a := find(Sweep(snap(claimed)), ActTransition, "T2"); a != nil && a.Marker != nil &&
		a.Marker.Fields["rule"] == "claim" {
		t.Errorf("the design agent's own claim was reverted: %+v", *a)
	}
}

// A queued ticket is waiting, not working. The order report separates
// "ready now" from "in flight", and Ready for design belongs on the
// first side — reading it as in flight is the confusion the split
// removed.
func TestTheDesignQueueIsNotInFlight(t *testing.T) {
	queued := tk("T1", protocol.ReadyForDesign)
	o := ComputeOrder(snap(queued), "")
	if got := layerOf(o, "T1"); got != LayerReady {
		t.Errorf("a queued ticket is in %q, want %q", got, LayerReady)
	}
}

// An agent state is written by a claim, so a hand-move into one is a §9
// violation and gets reverted. That only works when the pipeline has a
// record to judge the arrival against — and "no record means not judged"
// is deliberate, so a ticket created and dragged straight into Designing
// before the pipeline ever wrote to it was judged by nothing, dispatched
// by nothing (no agent state is a dispatch source) and timed out by
// nothing (the stale-claim rule needs a dead run). It sat.
func TestAnUnclaimedTicketInAnAgentStateGoesToItsQueue(t *testing.T) {
	for _, c := range []struct{ from, want protocol.State }{
		{protocol.Designing, protocol.ReadyForDesign},
		{protocol.InProgress, protocol.ReadyForDev},
		{protocol.Reworking, protocol.ReadyForRework},
	} {
		t.Run(string(c.from), func(t *testing.T) {
			stranded := tk("T1", c.from)
			s := snap(stranded)
			delete(s.Recorded, "T1") // never written by the pipeline

			a := find(Sweep(s), ActTransition, "T1")
			if a == nil {
				t.Fatalf("a stranded ticket in %s was left where it was", c.from)
			}
			if a.To != c.want {
				t.Errorf("moved to %q, want the queue that feeds that agent (%q)", a.To, c.want)
			}
		})
	}
}

// The two conditions are what keep this from overlapping the rules that
// already work, so each is checked from the other side.
func TestTheQueueRescueYieldsToTheRulesThatOwnTheTicket(t *testing.T) {
	// A record means revertFor owns it, and sending the ticket back where
	// it came from is the more precise answer than queueing it.
	judged := tk("T1", protocol.Designing, arrived(protocol.Todo, RoleAuthor))
	a := find(Sweep(snap(judged)), ActTransition, "T1")
	if a == nil || a.To != protocol.Todo {
		t.Errorf("a judged hand-move went to %v, want a revert to its origin", a)
	}

	// A live run means an agent is working; a dead one is the
	// stale-claim rule's. Neither is this rule's business.
	for _, live := range []bool{true, false} {
		running := tk("T2", protocol.Designing, func(t *Ticket) {
			t.Run = &Run{ID: "r1", Kind: AgentDesign, Live: live}
			if !live {
				t.Run.EndedAt = t0.Add(-time.Hour)
			}
		})
		s := snap(running)
		delete(s.Recorded, "T2")
		if a := find(Sweep(s), ActTransition, "T2"); a != nil && a.To == protocol.ReadyForDesign {
			t.Errorf("live=%v: queued a ticket a run had claimed", live)
		}
	}

	// The boundary ticket's In progress is the author's own signal that
	// their pass is done (DESIGN §10), and it has no queue at all.
	boundary := tk("T3", protocol.InProgress, func(t *Ticket) { t.Labels = []string{LabelBoundary} })
	s := snap(boundary)
	delete(s.Recorded, "T3")
	if a := find(Sweep(s), ActTransition, "T3"); a != nil && a.To == protocol.ReadyForDev {
		t.Error("queued the boundary ticket, whose In progress is the signal to run")
	}

	// Author-only work is the author's from Todo to Done (DESIGN §8).
	own := tk("T4", protocol.InProgress, func(t *Ticket) { t.Labels = []string{LabelAuthorOnly} })
	s = snap(own)
	delete(s.Recorded, "T4")
	if a := find(Sweep(s), ActTransition, "T4"); a != nil {
		t.Errorf("moved an author-only ticket: %+v", *a)
	}
}

// The flag has to survive the promotion, or reconciliation is handed a
// question nobody told it about. Nothing in the sweep strips it on the
// way through Checks.
func TestTheFlagTravelsIntoReconciling(t *testing.T) {
	flagged := tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIGreen, RunURL: "https://ci/1"}
		t.Labels = []string{LabelReEvaluate}
	})
	for _, a := range Sweep(snap(flagged)) {
		if a.TicketID == flagged.ID && a.Kind == ActRemoveLabel && a.Label == LabelReEvaluate {
			t.Error("the sweep cleared the flag on promotion; reconcile would never see it")
		}
	}
	if !flagged.HasLabel(LabelReEvaluate) {
		t.Error("the flag did not survive the pass")
	}
}

// A failing live suite parks the boundary ticket, because a comment was
// not a signal.
//
// Measured on Catapult's ORC-99: the run failed, posted its marker, and
// the ticket sat in Todo looking exactly as it would have looked on a
// pass, on a no-tests, or before the suite ran at all. Four situations,
// one appearance — and §10's reason for not surfacing it anywhere else
// was that the marker "lands on the boundary ticket where the author is
// already looking".
func TestFailingLiveSuiteBlocksTheBoundaryTicket(t *testing.T) {
	b := tk("B1", protocol.Todo,
		func(t *Ticket) { t.Labels = []string{LabelBoundary} },
		withComment(marker.LiveSuite, map[string]string{"result": "fail", "run": "https://ci/1"}))
	a := find(Sweep(snap(b)), ActTransition, "B1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want the boundary parked in Blocked, got %v", a)
	}
	if a.Marker == nil || a.Marker.Fields["live-suite"] == "" {
		t.Errorf("the block must say which flavor it is, got %v", a.Marker)
	}
	// The origin, so the author knows what they are returning to.
	if a.Marker.Fields["from"] != string(protocol.Todo) {
		t.Errorf("from = %q, want todo", a.Marker.Fields["from"])
	}
}

// Neither of the other two verdicts parks anything. `no-tests` is
// deliberately neither a pass nor a failure (DESIGN §10) and the author's
// pass decides whether the milestone closes without live coverage —
// blocking on it would make the first boundary of every new project red
// for a structural reason.
func TestOnlyAFailingLiveSuiteBlocks(t *testing.T) {
	for _, verdict := range []string{"pass", "no-tests"} {
		b := tk("B1", protocol.Todo,
			func(t *Ticket) { t.Labels = []string{LabelBoundary} },
			withComment(marker.LiveSuite, map[string]string{"result": verdict}))
		if a := find(Sweep(snap(b)), ActTransition, "B1"); a != nil {
			t.Errorf("%s: moved the ticket, got %v", verdict, a)
		}
	}
}

// The oscillation this rule has to survive. The author's move out of
// Blocked is theirs to choose (DESIGN §12), and Blocked -> Todo is the
// natural one: it says "seen it, back to my pass". The verdict is still
// `fail` at that moment, so a rule keyed on the verdict alone would park
// the ticket again on the very next sweep, forever, with the author
// unable to do anything about it.
func TestABlockedLiveSuiteIsNotReportedTwice(t *testing.T) {
	b := tk("B1", protocol.Todo,
		func(t *Ticket) { t.Labels = []string{LabelBoundary} },
		withComment(marker.LiveSuite, map[string]string{"result": "fail"}),
		withComment(marker.Blocked, map[string]string{"live-suite": "1", "from": "todo"}),
		arrived(protocol.Blocked, RoleAuthor))
	if a := find(Sweep(snap(b)), ActTransition, "B1"); a != nil {
		t.Fatalf("re-blocked a verdict already reported, got %v", a)
	}
}

// But a second failure is a second thing to say. A re-run posts a new
// marker, which puts the newest verdict after the report again.
func TestASecondLiveSuiteFailureBlocksAgain(t *testing.T) {
	b := tk("B1", protocol.InProgress,
		func(t *Ticket) { t.Labels = []string{LabelBoundary} },
		withComment(marker.LiveSuite, map[string]string{"result": "fail"}),
		withComment(marker.Blocked, map[string]string{"live-suite": "1", "from": "todo"}),
		withComment(marker.LiveSuite, map[string]string{"result": "fail", "run": "https://ci/2"}),
		arrived(protocol.Todo, RoleAuthor))
	a := find(Sweep(snap(b)), ActTransition, "B1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want the re-run's failure parked too, got %v", a)
	}
}

// Not while the re-run is still going. Parking the ticket underneath a
// live run tells the author to look at a result the pipeline is already
// redoing.
func TestALiveRunHoldsTheBlockBack(t *testing.T) {
	b := tk("B1", protocol.InProgress,
		func(t *Ticket) {
			t.Labels = []string{LabelBoundary}
			t.Run = &Run{ID: "r2", Kind: AgentLiveSuite, Live: true}
		},
		withComment(marker.LiveSuite, map[string]string{"result": "fail"}))
	if a := find(Sweep(snap(b)), ActTransition, "B1"); a != nil {
		t.Fatalf("blocked while the suite was still running, got %v", a)
	}
}

// The boundary agent must dispatch after the live suite it just waited
// for, and the run that satisfied it must not be read as its own.
//
// Measured on Catapult's ORC-99. The ticket entered In progress at
// 19:23:37, the re-run live suite passed at 19:26:09, and nothing
// dispatched afterwards: awaitingDispatch asks "has any run ended since
// this ticket arrived", the live-suite run had, and the boundary agent's
// turn never came. The ticket then sat until the stale-claim rule parked
// it — reporting accurately that no boundary run had ever been
// dispatched, which was the symptom rather than the cause.
//
// Every other state feeds exactly one agent, so the plain form is right
// everywhere else. The boundary ticket's In progress feeds two.
func TestBoundaryAgentDispatchesAfterTheLiveSuiteThatSatisfiedIt(t *testing.T) {
	b := tk("B1", protocol.InProgress,
		func(t *Ticket) {
			t.Labels = []string{LabelBoundary}
			// Entered In progress, then the suite ran and passed: the
			// run ends *after* the arrival, which is the whole bug.
			t.StateSince = t0.Add(-3 * time.Minute)
			t.Run = &Run{ID: "ls1", Kind: AgentLiveSuite, Live: false, EndedAt: t0.Add(-time.Minute)}
		},
		withComment(marker.LiveSuite, map[string]string{"result": "pass"}),
		arrived(protocol.Todo, RoleAuthor))
	a := find(Sweep(snap(b)), ActDispatch, "B1")
	if a == nil || a.Agent != AgentBoundary {
		t.Fatalf("dispatched %v, want the boundary agent", a)
	}
}

// And the converse still holds: another kind's run that is still live
// means the ticket is busy, so nothing else starts on top of it.
func TestALiveRunOfAnotherKindStillHoldsTheBoundaryAgent(t *testing.T) {
	b := tk("B1", protocol.InProgress,
		func(t *Ticket) {
			t.Labels = []string{LabelBoundary}
			t.Run = &Run{ID: "ls1", Kind: AgentLiveSuite, Live: true}
		},
		withComment(marker.LiveSuite, map[string]string{"result": "pass"}),
		arrived(protocol.Todo, RoleAuthor))
	if a := find(Sweep(snap(b)), ActDispatch, "B1"); a != nil {
		t.Fatalf("dispatched over a live run, got %v", a)
	}
}

// A re-run keeps the run's id and its URL and increments only the
// attempt, so a verdict keyed on the URL alone reads the second failure
// as the first one already recorded. That is silence on a real failure:
// the ticket sits in Checks with red CI and no comment saying so. The
// pipeline re-runs deliberately now, so this is reachable rather than
// theoretical.
func TestARerunsOwnFailureIsNotReadAsTheOneAlreadyRecorded(t *testing.T) {
	s := snap(tk("T1", protocol.Checks,
		withComment(marker.CIRed, map[string]string{"run": "https://ci/9", "run_attempt": "1", "attempt": "1"}),
		func(t *Ticket) { t.CI = CIInfo{Status: CIRed, RunURL: "https://ci/9", RunAttempt: 2} }))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil {
		t.Fatal("the re-run's own failure was swallowed as already recorded")
	}
	if a.Marker.Fields["run_attempt"] != "2" {
		t.Errorf("marker records run_attempt %q, want 2", a.Marker.Fields["run_attempt"])
	}
}

// The same attempt of the same run stays silent, which is the half the
// URL key got right and must keep.
func TestTheSameAttemptOfTheSameRunStaysSilent(t *testing.T) {
	s := snap(tk("T1", protocol.Checks,
		withComment(marker.CIRed, map[string]string{"run": "https://ci/9", "run_attempt": "2", "attempt": "1"}),
		func(t *Ticket) { t.CI = CIInfo{Status: CIRed, RunURL: "https://ci/9", RunAttempt: 2} }))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil {
		t.Errorf("recorded red re-fired: %v", *a)
	}
}

// Two tickets in a queue sharing a mutex label were each other's holder,
// so the sweep planned nothing and the dev agent sat idle — the mutex
// stalling both instead of serializing them. Measured on Catapult:
// ORC-171 and ORC-174 share system:delivery and both sat in Ready for
// rework from 2026-08-31T03:37:28Z to 05:12:52Z, an hour and thirty-five
// minutes, until the author moved one to Blocked by hand.
func TestIdleQueuedTicketsBreakTheTieByPrecedence(t *testing.T) {
	first := tk("T1", protocol.ReadyForRework, func(t *Ticket) { t.Labels = []string{"system:delivery"} })
	second := tk("T2", protocol.ReadyForRework, func(t *Ticket) { t.Labels = []string{"system:delivery"} })
	s := snap(first, second)
	if !Precedes(first, second) {
		t.Fatal("precondition: T1 must precede T2 for this test to say what it means")
	}

	if d := find(Sweep(s), ActDispatch, "T1"); d == nil {
		t.Error("the ticket precedence picks was not dispatched: the pair is still deadlocked")
	}
	if d := find(Sweep(s), ActDispatch, "T2"); d != nil {
		t.Errorf("both tickets dispatched — the mutex stopped serializing them: %v", *d)
	}

	// The dispatcher and the pickup assertion must agree, in both
	// directions. Disagreement is a full billed job spent to be refused.
	if err := VerifyPickup(s, first.ID, AgentDev, "r1"); err != nil {
		t.Errorf("dispatched T1 into a pickup that refuses it: %v", err)
	}
	if err := VerifyPickup(s, second.ID, AgentDev, "r2"); err == nil {
		t.Error("T2 was not dispatched but its pickup would have allowed it")
	}
}

// The tie-break yields to precedence only for a holder that is waiting.
// A holder with an agent on it, or one past the queues, still blocks
// unconditionally — that is the invariant DESIGN §6 exists for, and
// widening the tie-break to cover it would put two agents in one system.
func TestAWorkingHolderStillBlocksWhoeverPrecedesIt(t *testing.T) {
	queued := tk("T1", protocol.ReadyForRework, func(t *Ticket) { t.Labels = []string{"system:delivery"} })
	for _, st := range []protocol.State{
		protocol.InProgress, protocol.Reworking, protocol.Checks, protocol.Reconciling,
	} {
		holder := tk("T2", st, func(t *Ticket) { t.Labels = []string{"system:delivery"} })
		s := snap(queued, holder)
		if !Precedes(queued, holder) && st != protocol.InProgress {
			// Precedence favours the further-along ticket, so the queued
			// one loses anyway; the case worth pinning is the one where
			// it wins the tie-break and must still be refused.
			continue
		}
		if other, _ := MutexBlocker(s, queued); other == nil {
			t.Errorf("a holder in %s yielded to a ticket that precedes it", st)
		}
	}
	// And a holder that is idle but in the queue with a live run on it —
	// dispatched, not yet claimed — is working, not waiting.
	live := tk("T2", protocol.ReadyForRework, func(t *Ticket) {
		t.Labels = []string{"system:delivery"}
		t.LiveRuns = []Run{{ID: "r9", Kind: AgentDev, Live: true}}
		t.Run = &Run{ID: "r9", Kind: AgentDev, Live: true}
	})
	if other, _ := MutexBlocker(snap(queued, live), queued); other == nil {
		t.Error("a queued holder with a live run yielded: two dev agents in one system")
	}
}

// The promotion door keeps the strict reading of §6 deliberately: it is
// the preventive half, and the deadlock does not arrive through it. Both
// Catapult tickets reached Ready for rework by bouncing off CI, a path
// this door never sees, so relaxing it would widen what may queue on one
// system while rescuing nothing already stuck.
func TestPromotionDoorKeepsTheStrictReading(t *testing.T) {
	first := tk("T1", protocol.ReadyForDev, func(t *Ticket) { t.Labels = []string{"system:delivery"} })
	second := tk("T2", protocol.ReadyForRework, func(t *Ticket) { t.Labels = []string{"system:delivery"} })
	s := snap(first, second)
	if other, _ := MutexHolder(s, first); other == nil {
		t.Error("the promotion door stopped seeing an idle queued holder")
	}
	// The author promoting past it is still reverted — and the promoted
	// ticket is Urgent deliberately. Ready for rework outranks Ready for
	// dev in precedenceRank (70 to 40), so an ordinary promotion loses
	// the tie-break to any idle holder and the door's choice of function
	// changes nothing: the first version of this test asserted the revert
	// on a non-urgent ticket, and relaxing the door to MutexBlocker left
	// it passing. Urgent is the case where the promoting ticket wins
	// precedence, so it is the only one that tells the two apart.
	promoted := tk("T3", protocol.ReadyForDev, func(t *Ticket) {
		t.Labels = []string{"system:delivery"}
		t.Priority = urgentPriority
	}, arrived(protocol.DesignReview, RoleAuthor))
	held := tk("T4", protocol.ReadyForRework, func(t *Ticket) { t.Labels = []string{"system:delivery"} })
	if !Precedes(promoted, held) {
		t.Fatal("precondition: T3 must win the tie-break or this asserts nothing")
	}
	acts := Sweep(snap(promoted, held))
	rev := find(acts, ActTransition, "T3")
	if rev == nil || rev.To != protocol.DesignReview {
		t.Errorf("the author's promotion past a held mutex was not reverted: %v", acts)
	}
}

// `merged` is written at one site — reconcile, after it merges the PR
// itself — so a PR the author merges by hand leaves no marker, and three
// readers degrade in silence: the retro note records no commit, the
// rehearsal reset cannot revert what the note does not name, and the
// deploy check finds no SHA and lets the timeout escalate a shipped
// ticket to Blocked.
func TestAHandMergeGetsItsMarkerBackfilled(t *testing.T) {
	tk1 := tk("T1", protocol.Merged)
	tk1.HandMerges = []HandMerge{{SHA: "abc123", PR: "42"}}
	acts := Sweep(snap(tk1))

	c := find(acts, ActComment, "T1")
	if c == nil {
		t.Fatalf("no marker backfilled for a hand-merged ticket: %v", acts)
	}
	if c.Marker == nil || c.Marker.Kind != marker.Merged {
		t.Fatalf("backfilled the wrong marker: %+v", c.Marker)
	}
	if got := c.Marker.Fields["sha"]; got != "abc123" {
		t.Errorf("marker carries sha %q, want abc123", got)
	}
	if got := c.Marker.Fields["pr"]; got != "42" {
		t.Errorf("marker carries pr %q, want 42", got)
	}

	// Convergent: once the marker is on the ticket the build stops
	// reporting a hand merge, so the rule cannot re-fire and comment
	// every beat.
	settled := tk("T2", protocol.Merged, withComment(marker.Merged, map[string]string{"sha": "abc123", "pr": "42"}))
	if c := find(Sweep(snap(settled)), ActComment, "T2"); c != nil {
		t.Errorf("re-commented a ticket that already carries its marker: %v", *c)
	}
}

// A ticket merged, reverted by hand and merged again has two commits,
// and a note carrying one would leave half of it on main when the reset
// ran. Both are backfilled.
func TestEveryHandMergeIsRecordedNotJustTheNewest(t *testing.T) {
	tk1 := tk("T1", protocol.Merged)
	tk1.HandMerges = []HandMerge{{SHA: "first", PR: "1"}, {SHA: "second", PR: "2"}}
	var shas []string
	for _, a := range Sweep(snap(tk1)) {
		if a.Kind == ActComment && a.Marker != nil && a.Marker.Kind == marker.Merged {
			shas = append(shas, a.Marker.Fields["sha"])
		}
	}
	if len(shas) != 2 || shas[0] != "first" || shas[1] != "second" {
		t.Errorf("backfilled %v, want both commits oldest first", shas)
	}
}

// MergedSHAs is the one reader the retro note and the deploy check now
// share, and they differ only in which end of it they take.
func TestMergedSHAsReadsEveryMarkerInOrder(t *testing.T) {
	ticket := tk("T1", protocol.Merged,
		withComment(marker.Merged, map[string]string{"sha": "one"}),
		withComment(marker.ReconcileBounce, map[string]string{"pr": "3"}),
		withComment(marker.Merged, map[string]string{"sha": "two"}))
	got := MergedSHAs(ticket)
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("MergedSHAs = %v, want [one two] — oldest first, other markers skipped", got)
	}
	if len(MergedSHAs(tk("T2", protocol.Merged))) != 0 {
		t.Error("a ticket with no comments reported a merge")
	}
}

// The record review's second decline on one ticket (DESIGN §4, §12).
// Mirrors the bounce trio above, because it is the same rule shape: a
// marker-counted escalation that fires once per count and leaves the
// author's return alone.

func TestSecondRecordReviewDeclineBlocks(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDesign,
		withComment(marker.RecordReview, map[string]string{"verdict": "decline"}),
		withComment(marker.RecordReview, map[string]string{"verdict": "decline"})))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want second decline -> Blocked, got %v", a)
	}
	if a.Marker == nil || a.Marker.Fields["declines"] != "2" {
		t.Errorf("want the escalation to record declines=2, got %v", a.Marker)
	}
}

// A pass is posted under the same marker kind so the author can see the
// review ran. It must not count: two checked passes are not two
// disagreements.
func TestRecordReviewPassesDoNotCountAsDeclines(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDesign,
		withComment(marker.RecordReview, map[string]string{"verdict": "pass"}),
		withComment(marker.RecordReview, map[string]string{"verdict": "decline"}),
		withComment(marker.RecordReview, map[string]string{"verdict": "pass"})))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil && a.To == protocol.Blocked {
		t.Errorf("escalated on one decline and two passes: %v", *a)
	}
	// One decline is the ordinary rework loop: the ticket is dispatched,
	// not parked.
	if a := find(Sweep(s), ActDispatch, "T1"); a == nil || a.Agent != AgentDesign {
		t.Errorf("no design run dispatched after a single decline: %v", Sweep(s))
	}
}

func TestTheAuthorsReturnToDesignAfterADeclineBlockIsNotReEscalated(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDesign,
		withComment(marker.RecordReview, map[string]string{"verdict": "decline"}),
		withComment(marker.RecordReview, map[string]string{"verdict": "decline"}),
		withComment(marker.Blocked, map[string]string{"from": "ready_for_design", "declines": "2"})))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil && a.To == protocol.Blocked {
		t.Errorf("re-blocked the author's return with no new decline: %v", *a)
	}
	if a := find(Sweep(s), ActDispatch, "T1"); a == nil || a.Agent != AgentDesign {
		t.Errorf("no design run dispatched for the returned ticket: %v", Sweep(s))
	}
}

func TestAThirdRecordReviewDeclineEscalatesAgain(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForDesign,
		withComment(marker.RecordReview, map[string]string{"verdict": "decline"}),
		withComment(marker.RecordReview, map[string]string{"verdict": "decline"}),
		withComment(marker.Blocked, map[string]string{"from": "ready_for_design", "declines": "2"}),
		withComment(marker.RecordReview, map[string]string{"verdict": "decline"})))
	a := find(Sweep(s), ActTransition, "T1")
	if a == nil || a.To != protocol.Blocked {
		t.Fatalf("want a third decline -> Blocked, got %v", a)
	}
	if a.Marker == nil || a.Marker.Fields["declines"] != "3" {
		t.Errorf("want the escalation to record declines=3, got %v", a.Marker)
	}
}

// Ready for redesign is a queue the design dispatcher reads exactly as
// it reads Ready for design: a record-review decline or a demote lands
// there so the bounce is visible in the state column rather than only
// in a marker, and a ticket parked in a state nobody dispatches from is
// the failure Designing-as-queue already had once.
func TestDesignDispatchesFromTheRedesignQueue(t *testing.T) {
	d := find(Sweep(snap(tk("T1", protocol.ReadyForRedesign))), ActDispatch, "T1")
	if d == nil || d.Agent != AgentDesign {
		t.Fatalf("a ticket in Ready for redesign was not dispatched: %v", d)
	}
}

// A bounced design reached the record review once, so it outranks a
// fresh one the way rework outranks fresh dev (DESIGN §7). The
// dispatcher walks tickets in precedence order and takes the first, so
// the rank is what puts the redesign in front — an older fresh design
// does not win on age.
func TestARedesignIsDispatchedBeforeAFreshDesign(t *testing.T) {
	fresh := tk("T1", protocol.ReadyForDesign, func(t *Ticket) { t.CreatedAt = t0.Add(-48 * time.Hour) })
	bounced := tk("T2", protocol.ReadyForRedesign)
	acts := Sweep(snap(fresh, bounced))
	if d := find(acts, ActDispatch, "T2"); d == nil || d.Agent != AgentDesign {
		t.Fatalf("the redesign was not the one dispatched: %v", acts)
	}
	if d := find(acts, ActDispatch, "T1"); d != nil {
		t.Errorf("the fresh design was dispatched alongside the redesign (the agent is singular): %+v", *d)
	}
}

// The queue rescue sends an unclaimed Designing ticket back to the queue
// it was claimed from. One claimed out of Ready for redesign goes back
// there, so the bounce it carries stays visible; sending it to Ready for
// design would quietly promote a redesign into a fresh design.
func TestAnUnclaimedDesignReturnsToTheQueueItCameFrom(t *testing.T) {
	for _, c := range []struct{ from, want protocol.State }{
		{protocol.ReadyForDesign, protocol.ReadyForDesign},
		{protocol.ReadyForRedesign, protocol.ReadyForRedesign},
	} {
		t.Run(string(c.from), func(t *testing.T) {
			stranded := tk("T1", protocol.Designing, arrived(c.from, RoleDesign))
			s := snap(stranded)
			delete(s.Recorded, "T1") // the tracker's history says where it came from; the pipeline never wrote it
			a := find(Sweep(s), ActTransition, "T1")
			if a == nil {
				t.Fatalf("a stranded Designing ticket from %s was left where it was", c.from)
			}
			if a.To != c.want {
				t.Errorf("moved to %q, want the queue it was claimed from (%q)", a.To, c.want)
			}
		})
	}
}

// The claim assertion mirrors the dispatcher: design may pick up from
// either queue, and nothing else may pick up from the redesign queue.
func TestPickupAdmitsTheRedesignQueueForDesignOnly(t *testing.T) {
	s := snap(tk("T1", protocol.ReadyForRedesign))
	if err := VerifyPickup(s, "T1", AgentDesign, "r1"); err != nil {
		t.Errorf("design refused the redesign queue: %v", err)
	}
	for _, kind := range []AgentKind{AgentDev, AgentReconcile} {
		if err := VerifyPickup(s, "T1", kind, "r1"); err == nil {
			t.Errorf("%s picked up from the redesign queue", kind)
		}
	}
}
