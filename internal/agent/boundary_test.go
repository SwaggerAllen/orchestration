package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

func TestBoundaryFullPass(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	now := time.Now()

	tr.AddMilestone(cfg.Tracker.ProjectID, "M: alpha", 1)
	done := seed(t, tr, cfg, "Shipped thing", "d", protocol.Done)
	if err := tr.Mutate(done.ID, func(i *tracker.Issue) { i.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}
	debt := seed(t, tr, cfg, "Old debt", "d", protocol.Backlog)

	// The boundary ticket, as the sweep would have created it, moved to
	// In progress by the author (the signal).
	boundary, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Milestone boundary — M: alpha", Description: "machinery",
		StateID: stateID(t, tr, cfg, protocol.InProgress),
		Labels:  []string{"milestone-boundary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Mutate(boundary.ID, func(i *tracker.Issue) { i.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}

	plan, err := ClaimBoundary(ctx, p, boundary.Key, "run_60", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Milestone != "M: alpha" || len(plan.Done) != 0 {
		t.Fatalf("plan = %+v", plan)
	}

	if err := BoundaryArchive(ctx, p, h, plan, now); err != nil {
		t.Fatal(err)
	}
	if !tr.Archived(done.ID) {
		t.Error("Done issue not archived")
	}
	if tr.Archived(boundary.ID) {
		t.Error("the boundary ticket must never archive itself — it is the record of the pass")
	}
	retro, ok := h.Files["docs/retros/m-alpha.md"]
	if !ok || !strings.Contains(retro, done.Key) {
		t.Errorf("retro note missing or incomplete: %q", retro)
	}

	ps := &Proposals{
		Proposals: []Proposal{
			{Title: "Extract cap module", Description: "gating", Kind: "debt", Gating: true, Dedupe: "M: alpha/extract-cap"},
			{Title: "Cap screen empty state", Description: "finding", Kind: "design", Dedupe: "M: alpha/cap-empty"},
		},
		Ranking: []RankEntry{{Key: debt.Key, Priority: 2}},
	}
	if err := BoundaryFile(ctx, p, plan, ps, now); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, boundary.ID); got != protocol.BoundaryReview {
		t.Errorf("state = %q, want boundary_review", got)
	}

	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	proposals := 0
	for _, i := range issues {
		if strings.Contains(i.Description, "[pipeline:v1:triage-proposal]") {
			proposals++
		}
		if i.ID == debt.ID && i.Priority != 2 {
			t.Errorf("ranking not applied: priority = %d", i.Priority)
		}
	}
	if proposals != 2 {
		t.Errorf("proposals filed = %d, want 2", proposals)
	}
}

// The dangerous re-run (DESIGN 10): a resumed boundary must not file
// proposals twice, and a resumed archive must not duplicate the retro.
func TestBoundaryResumeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	now := time.Now()

	tr.AddMilestone(cfg.Tracker.ProjectID, "M: alpha", 1)
	boundary, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Milestone boundary — M: alpha", Description: "machinery",
		StateID: stateID(t, tr, cfg, protocol.InProgress),
		Labels:  []string{"milestone-boundary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Mutate(boundary.ID, func(i *tracker.Issue) { i.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}

	plan, err := ClaimBoundary(ctx, p, boundary.Key, "run_61", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := BoundaryArchive(ctx, p, h, plan, now); err != nil {
		t.Fatal(err)
	}
	ps := &Proposals{Proposals: []Proposal{
		{Title: "Extract cap module", Description: "x", Kind: "debt", Dedupe: "M: alpha/extract-cap"},
	}}
	if err := BoundaryFile(ctx, p, plan, ps, now); err != nil {
		t.Fatal(err)
	}

	// Crash simulation: the author routes it back (Blocked -> In progress
	// per DESIGN 10's recovery), and the whole pass re-runs from claim.
	if err := tr.UpdateIssueState(ctx, boundary.ID, stateID(t, tr, cfg, protocol.InProgress)); err != nil {
		t.Fatal(err)
	}
	plan2, err := ClaimBoundary(ctx, p, boundary.Key, "run_62", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.Done[StepArchive] || !plan2.Done[StepScan] || !plan2.Done[StepFile] {
		t.Fatalf("resume must see completed steps: %+v", plan2.Done)
	}
	if err := BoundaryArchive(ctx, p, h, plan2, now); err != nil {
		t.Fatal(err)
	}
	// Filing again with nil proposals recovers from the scan comment and
	// the dedupe keys keep it a no-op.
	if err := BoundaryFile(ctx, p, plan2, nil, now); err != nil {
		t.Fatal(err)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	proposals := 0
	for _, i := range issues {
		if strings.Contains(i.Description, "M: alpha/extract-cap") {
			proposals++
		}
	}
	if proposals != 1 {
		t.Errorf("proposal filed %d times across a resume, want exactly 1", proposals)
	}
}

func TestParseProposalsRejectsBugs(t *testing.T) {
	_, err := ParseProposals([]byte(`{"proposals":[{"title":"Crash","kind":"bug","dedupe":"m/crash"}]}`))
	if err == nil || !strings.Contains(err.Error(), "never bugs") {
		t.Errorf("bugs must never park in Triage, got %v", err)
	}
}

// seedBoundary makes a boundary ticket the way the sweep would.
func seedBoundary(t *testing.T, tr *tracker.Memory, cfg *config.Config) tracker.Issue {
	t.Helper()
	i, err := tr.CreateIssue(context.Background(), tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Milestone boundary — M1", Description: "machinery",
		StateID: stateID(t, tr, cfg, protocol.InProgress),
		Labels:  []string{"milestone-boundary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Mutate(i.ID, func(is *tracker.Issue) { is.Milestone = "M1" }); err != nil {
		t.Fatal(err)
	}
	return i
}
