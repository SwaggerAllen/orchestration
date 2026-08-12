package setup

import (
	"context"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

func TestRunProvisionsFreshTeam(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()

	actions, err := Run(ctx, tr, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	wantActions := len(protocol.AllStates) + len(protocol.Labels)
	if len(actions) != wantActions {
		t.Errorf("actions = %d, want %d", len(actions), wantActions)
	}

	states, _ := tr.ListStates(ctx, cfg.Tracker.TeamID)
	if len(states) != len(protocol.AllStates) {
		t.Errorf("states created = %d, want %d", len(states), len(protocol.AllStates))
	}
	labels, _ := tr.ListLabels(ctx, cfg.Tracker.TeamID)
	if len(labels) != len(protocol.Labels) {
		t.Errorf("labels created = %d, want %d", len(labels), len(protocol.Labels))
	}
}

// The M0 gate: a second run is a no-op.
func TestRunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()

	if _, err := Run(ctx, tr, cfg, false); err != nil {
		t.Fatal(err)
	}
	again, err := Run(ctx, tr, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second run planned %d actions, want 0: %v", len(again), again)
	}
}

func TestPlanCreatesOnlyWhatIsMissing(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()

	// Linear teams come with defaults; simulate a few pre-existing pieces.
	if _, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{Name: "Backlog", Category: protocol.CategoryBacklog}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.CreateLabel(ctx, cfg.Tracker.TeamID, "bug"); err != nil {
		t.Fatal(err)
	}

	actions, err := Plan(ctx, tr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := (len(protocol.AllStates) - 1) + (len(protocol.Labels) - 1)
	if len(actions) != want {
		t.Errorf("actions = %d, want %d", len(actions), want)
	}
	for _, a := range actions {
		if a.Op == CreateState && a.Name == "Backlog" {
			t.Error("planned to create a state that exists")
		}
		if a.Op == CreateLabel && a.Name == "bug" {
			t.Error("planned to create a label that exists")
		}
	}
}

// The workspace half of the label namespace. Linear's workspace-level
// labels belong to no team, and a team-scoped read misses them — which is
// how setup came to plan a create the API rejected as a duplicate.
func TestPlanAdoptsWorkspaceLabels(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()

	for _, name := range []string{"frontend", "backend", "tech-debt", "design-inbox"} {
		if _, err := tr.AddWorkspaceLabel(name); err != nil {
			t.Fatal(err)
		}
	}

	actions, err := Plan(ctx, tr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.Op == CreateLabel {
			switch a.Name {
			case "frontend", "backend", "tech-debt", "design-inbox":
				t.Errorf("planned to create workspace label %q — Linear rejects that as a duplicate", a.Name)
			}
		}
	}
	if err := Apply(ctx, tr, cfg, actions); err != nil {
		t.Fatalf("apply over an adopted workspace taxonomy: %v", err)
	}
	again, err := Plan(ctx, tr, cfg)
	if err != nil || len(again) != 0 {
		t.Errorf("second plan = %d actions, %v; want a no-op", len(again), err)
	}
}

// A name that differs only in case is one taxonomy split in two. Setup
// does not rename, so it must stop rather than create the near-duplicate.
func TestPlanRefusesCaseVariantLabel(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()

	if _, err := tr.AddWorkspaceLabel("Bug"); err != nil {
		t.Fatal(err)
	}
	_, err := Plan(ctx, tr, cfg)
	if err == nil || !strings.Contains(err.Error(), "near-duplicate") {
		t.Errorf("want a refusal naming the conflict, got %v", err)
	}
}

func TestPlanRefusesCategoryConflict(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()

	// "Merged" exists but as a completed state — a live conflict the
	// command must surface, not repair.
	if _, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{Name: "Merged", Category: protocol.CategoryCompleted}); err != nil {
		t.Fatal(err)
	}

	_, err := Plan(ctx, tr, cfg)
	if err == nil || !strings.Contains(err.Error(), "will not retype") {
		t.Errorf("want retype refusal, got %v", err)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()

	actions, err := Run(ctx, tr, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) == 0 {
		t.Fatal("dry run should still plan actions")
	}
	states, _ := tr.ListStates(ctx, cfg.Tracker.TeamID)
	labels, _ := tr.ListLabels(ctx, cfg.Tracker.TeamID)
	if len(states) != 0 || len(labels) != 0 {
		t.Errorf("dry run mutated the tracker: %d states, %d labels", len(states), len(labels))
	}
}

// The palette exists to answer one question at a glance: is anything
// waiting on me? Colours are shared on purpose — In Progress and
// Reworking are both the dev agent, Done and Canceled are both terminal
// — so the property worth pinning is not uniqueness. It is that nothing
// the author must act on looks like anything they need not.
func TestNothingNeedingTheAuthorLooksLikeAnythingElse(t *testing.T) {
	for _, ps := range protocol.AllStates {
		if protocol.Colors[ps] == "" {
			t.Errorf("state %q has no colour", ps)
		}
	}

	mine := map[protocol.State]bool{
		protocol.DesignReview:   true,
		protocol.BoundaryReview: true,
		protocol.Blocked:        true,
	}
	for ps := range mine {
		for _, other := range protocol.AllStates {
			if mine[other] {
				continue
			}
			if protocol.Colors[ps] == protocol.Colors[other] {
				t.Errorf("%q (needs the author) is the same colour as %q — the board stops answering the only question it is for", ps, other)
			}
		}
	}
	// And within the author's own set, stuck must not read as queued.
	if protocol.Colors[protocol.Blocked] == protocol.Colors[protocol.DesignReview] {
		t.Error("Blocked should not look like an ordinary review — one is stuck, the other is waiting its turn")
	}
}

// Setup must carry the colour through to the tracker, or the palette is
// decoration in a file nobody reads.
func TestApplyCreatesStatesWithTheirColour(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()

	if _, err := Run(ctx, tr, cfg, false); err != nil {
		t.Fatal(err)
	}
	states, _ := tr.ListStates(ctx, cfg.Tracker.TeamID)
	byName := map[string]tracker.StateInfo{}
	for _, s := range states {
		byName[s.Name] = s
	}
	for _, ps := range protocol.AllStates {
		if got := byName[cfg.StateName(ps)].Color; got != protocol.Colors[ps] {
			t.Errorf("%s created with colour %q, want %q", ps, got, protocol.Colors[ps])
		}
	}
}
