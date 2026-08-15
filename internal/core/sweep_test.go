package core

import (
	"os"
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
		t.Fatalf("the result marker ends the loop — no dispatch wanted, got %v", a)
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

func TestLiveSuiteNotDispatchedPastTodo(t *testing.T) {
	b := tk("B1", protocol.InProgress,
		func(t *Ticket) { t.Labels = []string{LabelBoundary} },
		arrived(protocol.Todo, RoleAuthor))
	acts := Sweep(snap(b))
	for _, a := range acts {
		if a.Kind == ActDispatch && a.Agent == AgentLiveSuite {
			t.Fatalf("live suite dispatches only in Todo, got %v", a)
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
