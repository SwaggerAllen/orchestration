package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// seedDebt files a tech-debt ticket the way the boundary would, so the
// gating judgment reads back off its own marker.
func seedDebt(t *testing.T, tr *tracker.Memory, cfg *config.Config, title string, gating bool, priority int) tracker.Issue {
	t.Helper()
	ctx := context.Background()
	m := marker.Marker{Kind: marker.TriageProposal, Fields: map[string]string{
		"dedupe": "M1/" + title, "gating": map[bool]string{true: "true", false: "false"}[gating],
	}}
	i, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: title, Description: m.Format() + "\n\nthe argument",
		StateID: stateID(t, tr, cfg, protocol.Backlog), Labels: []string{"tech-debt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.UpdateIssuePriority(ctx, i.ID, priority); err != nil {
		t.Fatal(err)
	}
	return i
}

func compositionComment(t *testing.T, tr *tracker.Memory, cfg *config.Config, boundaryID string) (marker.Marker, string) {
	t.Helper()
	issues, _ := tr.ListIssues(context.Background(), cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	for _, i := range issues {
		if i.ID != boundaryID {
			continue
		}
		for _, c := range i.Comments {
			if m, ok, err := marker.Parse(c.Body); err == nil && ok && m.Kind == marker.Composition {
				return m, c.Body
			}
		}
	}
	t.Fatal("no composition comment on the boundary ticket")
	return marker.Marker{}, ""
}

func TestCompositionTakesAllGatingPlusTheFloor(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	if _, err := tr.CreateLabel(ctx, cfg.Tracker.TeamID, "tech-debt"); err != nil {
		_ = err // setup already created it
	}

	seedDebt(t, tr, cfg, "gating one", true, 3)
	seedDebt(t, tr, cfg, "gating two", true, 4)
	// Seven non-gating: only the floor of five should be drawn, highest
	// priority first (1 = Urgent).
	for i, prio := range []int{4, 1, 3, 2, 4, 3, 0} {
		seedDebt(t, tr, cfg, "non-gating "+string(rune('a'+i)), false, prio)
	}

	boundary := seedBoundary(t, tr, cfg)
	plan := &BoundaryPlan{ClaimResult: ClaimResult{TicketID: boundary.ID}, Milestone: "M1"}
	if err := ProposeComposition(ctx, p, plan, time.Now()); err != nil {
		t.Fatal(err)
	}

	m, body := compositionComment(t, tr, cfg, boundary.ID)
	if m.Fields["gating"] != "2" || m.Fields["non_gating"] != "5" {
		t.Errorf("counts = gating %q non-gating %q, want 2 and 5", m.Fields["gating"], m.Fields["non_gating"])
	}
	if got := len(strings.Fields(m.Fields["keys"])); got != 7 {
		t.Errorf("keys = %d, want 7 (all gating + the floor)", got)
	}
	// The Urgent non-gating ticket must be drawn; a priority-0 one must not.
	if !strings.Contains(body, "priority 1)") {
		t.Error("highest-priority non-gating ticket not drawn")
	}
	if strings.Contains(body, "priority 0)") {
		t.Error("no-priority ticket drawn ahead of prioritised ones")
	}
}

func TestCompositionReportsAThinPool(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	seedDebt(t, tr, cfg, "only debt", false, 2)

	boundary := seedBoundary(t, tr, cfg)
	plan := &BoundaryPlan{ClaimResult: ClaimResult{TicketID: boundary.ID}, Milestone: "M1"}
	if err := ProposeComposition(ctx, p, plan, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, body := compositionComment(t, tr, cfg, boundary.ID)
	if !strings.Contains(body, "below the floor") {
		t.Errorf("a thin pool must say so rather than pad: %q", body)
	}
}

func TestCompositionSkipsScheduledAndResolvedDebt(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	tr.AddMilestone(cfg.Tracker.ProjectID, "Debt: already", 1)

	scheduled := seedDebt(t, tr, cfg, "already scheduled", true, 2)
	if err := tr.Mutate(scheduled.ID, func(i *tracker.Issue) { i.Milestone = "Debt: already" }); err != nil {
		t.Fatal(err)
	}
	resolved := seedDebt(t, tr, cfg, "already done", true, 2)
	if err := tr.UpdateIssueState(ctx, resolved.ID, stateID(t, tr, cfg, protocol.Done)); err != nil {
		t.Fatal(err)
	}

	boundary := seedBoundary(t, tr, cfg)
	plan := &BoundaryPlan{ClaimResult: ClaimResult{TicketID: boundary.ID}, Milestone: "M1"}
	if err := ProposeComposition(ctx, p, plan, time.Now()); err != nil {
		t.Fatal(err)
	}
	m, _ := compositionComment(t, tr, cfg, boundary.ID)
	if m.Fields["keys"] != "" {
		t.Errorf("scheduled and resolved debt must not be re-proposed, got keys %q", m.Fields["keys"])
	}
}

// The composition is posted after the file step precisely so it can name
// what that step just created — and it could not see any of it. Filed
// proposals land in a triage-category state, which Build skips, so a
// composition running seconds later read a backlog with nothing in it.
//
// Catapult's ORC-45, second pass, printed the two lines together:
//
//	[boundary-step] step=file    Filed 8 proposals (0 deduped)
//	[composition] gating=0 keys="" non_gating=0
//	              Nothing to schedule — no unscheduled tech-debt tickets.
//
// Invisible before Triage was enabled on the team, because the filer
// falls back to Backlog when no triage state exists and Backlog is a
// state Build maps.
func TestCompositionSeesProposalsStillSittingInTriage(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	triage, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{
		Name: "Triage", Category: protocol.CategoryTriage, Color: "#000000",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Filed by this boundary and not yet accepted — where every proposal
	// sits at the moment the composition runs.
	fresh := seedDebt(t, tr, cfg, "gating and unaccepted", true, 3)
	if err := tr.UpdateIssueState(ctx, fresh.ID, triage.ID); err != nil {
		t.Fatal(err)
	}
	accepted := seedDebt(t, tr, cfg, "accepted earlier", false, 2)

	boundary := seedBoundary(t, tr, cfg)
	plan := &BoundaryPlan{ClaimResult: ClaimResult{TicketID: boundary.ID}, Milestone: "M1"}
	if err := ProposeComposition(ctx, p, plan, time.Now()); err != nil {
		t.Fatal(err)
	}

	m, body := compositionComment(t, tr, cfg, boundary.ID)
	keys := strings.Fields(m.Fields["keys"])
	for _, want := range []string{fresh.Key, accepted.Key} {
		var found bool
		for _, k := range keys {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Errorf("composition omits %s; keys = %v\n%s", want, keys, body)
		}
	}
	if m.Fields["gating"] != "1" {
		t.Errorf("gating = %q, want 1 — the unaccepted proposal is a gating candidate", m.Fields["gating"])
	}
}
