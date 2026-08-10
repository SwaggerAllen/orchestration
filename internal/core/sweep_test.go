package core

import (
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func snap(tickets ...*Ticket) *Snapshot {
	return &Snapshot{
		Now:              t0,
		CurrentMilestone: "M1",
		StaleClaimGrace:  20 * time.Minute,
		DeployTimeout:    30 * time.Minute,
		Tickets:          tickets,
	}
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

func TestCIGreenHeldByReEvaluate(t *testing.T) {
	s := snap(tk("T1", protocol.Checks, func(t *Ticket) {
		t.CI = CIInfo{Status: CIGreen}
		t.Labels = []string{LabelReEvaluate}
	}))
	if a := find(Sweep(s), ActTransition, "T1"); a != nil {
		t.Errorf("re-evaluate must hold promotion, got %v", *a)
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
		t.Errorf("want second bounce -> Blocked, got %v", a)
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
	boundary := tk("B1", protocol.InProgress, func(t *Ticket) {
		t.Labels = []string{LabelBoundary}
	}, arrived(protocol.Todo, RoleAuthor))
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
