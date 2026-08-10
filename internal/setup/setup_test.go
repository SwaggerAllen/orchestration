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
	if _, err := tr.CreateState(ctx, cfg.Tracker.TeamID, "Backlog", protocol.CategoryBacklog); err != nil {
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

func TestPlanRefusesCategoryConflict(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()

	// "Merged" exists but as a completed state — a live conflict the
	// command must surface, not repair.
	if _, err := tr.CreateState(ctx, cfg.Tracker.TeamID, "Merged", protocol.CategoryCompleted); err != nil {
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
