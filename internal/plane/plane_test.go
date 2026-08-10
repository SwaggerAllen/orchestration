package plane

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/setup"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// world provisions a fake tracker exactly the way production does: through
// setup. Tests then act on it through the same port the live plane uses.
func world(t *testing.T) (*tracker.Memory, *config.Config, *Plane) {
	t.Helper()
	tr := tracker.NewMemory()
	cfg := config.Sample()
	if _, err := setup.Run(context.Background(), tr, cfg, false); err != nil {
		t.Fatal(err)
	}
	return tr, cfg, New(tr, cfg)
}

func stateID(t *testing.T, tr *tracker.Memory, cfg *config.Config, s protocol.State) string {
	t.Helper()
	states, err := tr.ListStates(context.Background(), cfg.Tracker.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.Name == cfg.StateName(s) {
			return st.ID
		}
	}
	t.Fatalf("no state %q", s)
	return ""
}

func seedIssue(t *testing.T, tr *tracker.Memory, cfg *config.Config, title string, s protocol.State) tracker.Issue {
	t.Helper()
	i, err := tr.CreateIssue(context.Background(), tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: title, StateID: stateID(t, tr, cfg, s),
	})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestBuildTranslatesStatesRolesAndHistory(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	i := seedIssue(t, tr, cfg, "Rework the cap screen", protocol.DesignReview)

	// The author signs off: a transition performed by the author's id.
	tr.ActorID = "usr_author"
	if err := tr.UpdateIssueState(ctx, i.ID, stateID(t, tr, cfg, protocol.ReadyForDev)); err != nil {
		t.Fatal(err)
	}
	tr.ActorID = "memory-bot"

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tickets) != 1 {
		t.Fatalf("tickets = %d", len(snap.Tickets))
	}
	tk := snap.Tickets[0]
	if tk.State != protocol.ReadyForDev {
		t.Errorf("state = %q", tk.State)
	}
	if tk.Last == nil || tk.Last.Actor != core.RoleAuthor || tk.Last.From != protocol.DesignReview {
		t.Errorf("last transition = %+v, want author from design_review", tk.Last)
	}
}

func TestBuildFailsOnUnmappedState(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	rogue, err := tr.CreateState(ctx, cfg.Tracker.TeamID, "Triage Later", protocol.CategoryStarted)
	if err != nil {
		t.Fatal(err)
	}
	i := seedIssue(t, tr, cfg, "Lost ticket", protocol.Todo)
	if err := tr.UpdateIssueState(ctx, i.ID, rogue.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Build(ctx, time.Now(), false); err == nil || !strings.Contains(err.Error(), "doesn't map") {
		t.Errorf("want unmapped-state error, got %v", err)
	}
}

func TestBuildCurrentMilestone(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	m1 := tr.AddMilestone(cfg.Tracker.ProjectID, "M: alpha", 1)
	tr.AddMilestone(cfg.Tracker.ProjectID, "M: beta", 2)

	i := seedIssue(t, tr, cfg, "Only ticket", protocol.Done)
	if err := tr.Mutate(i.ID, func(is *tracker.Issue) { is.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}
	_ = m1

	// Drained milestone without a Done boundary is still current.
	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.CurrentMilestone != "M: alpha" {
		t.Errorf("current = %q, want M: alpha", snap.CurrentMilestone)
	}
}

// The M2 composition test: seed an illegal promotion in the fake tracker,
// then Build -> Sweep -> Execute -> Build and watch the revert land as
// tracker state — the same loop the live control plane runs.
func TestLiveShapedLoopRevertsIllegalPromotion(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	inFlight := seedIssue(t, tr, cfg, "Holder", protocol.InProgress)
	if _, err := tr.CreateLabel(ctx, cfg.Tracker.TeamID, "screen:home"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AddIssueLabel(ctx, cfg.Tracker.TeamID, inFlight.ID, "screen:home"); err != nil {
		t.Fatal(err)
	}

	victim := seedIssue(t, tr, cfg, "Collider", protocol.DesignReview)
	if err := tr.AddIssueLabel(ctx, cfg.Tracker.TeamID, victim.ID, "screen:home"); err != nil {
		t.Fatal(err)
	}
	tr.ActorID = "usr_author"
	if err := tr.UpdateIssueState(ctx, victim.ID, stateID(t, tr, cfg, protocol.ReadyForDev)); err != nil {
		t.Fatal(err)
	}
	tr.ActorID = "memory-bot"

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	acts := core.Sweep(snap)
	var log bytes.Buffer
	if err := p.Execute(ctx, acts, &log); err != nil {
		t.Fatal(err)
	}

	after, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range after.Tickets {
		if tk.ID != victim.ID {
			continue
		}
		if tk.State != protocol.DesignReview {
			t.Errorf("victim state = %q, want reverted to design_review", tk.State)
		}
		found := false
		for _, c := range tk.Comments {
			if strings.Contains(c.Body, "[pipeline:v1:revert]") {
				found = true
			}
		}
		if !found {
			t.Error("revert marker comment missing")
		}
	}
}

func TestExecuteCreatesBoundaryTicket(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	tr.AddMilestone(cfg.Tracker.ProjectID, "M: alpha", 1)
	i := seedIssue(t, tr, cfg, "Last one", protocol.Done)
	if err := tr.Mutate(i.ID, func(is *tracker.Issue) { is.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	acts := core.Sweep(snap)
	var log bytes.Buffer
	if err := p.Execute(ctx, acts, &log); err != nil {
		t.Fatal(err)
	}

	issues, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var boundary *tracker.Issue
	for idx := range issues {
		for _, l := range issues[idx].Labels {
			if l == "milestone-boundary" {
				boundary = &issues[idx]
			}
		}
	}
	if boundary == nil {
		t.Fatal("boundary ticket not created")
	}
	if boundary.Milestone != "M: alpha" || !strings.Contains(boundary.Description, "pipeline machinery") {
		t.Errorf("boundary = %+v", boundary)
	}

	// Second loop: boundary exists, so nothing new is planned.
	snap2, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range core.Sweep(snap2) {
		if a.Kind == core.ActCreateBoundary {
			t.Errorf("boundary planned twice: %v", a)
		}
	}
}
