package main

import (
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// liveTable is the team as setup would have left it: every protocol
// state, under the name this config maps it to, in the right category.
func liveTable(cfg *config.Config) []tracker.StateInfo {
	var out []tracker.StateInfo
	for _, ps := range protocol.AllStates {
		out = append(out, tracker.StateInfo{
			ID:       "st_" + string(ps),
			Name:     cfg.StateName(ps),
			Category: protocol.Categories[ps],
		})
	}
	return out
}

func TestAProvisionedTeamPassesTheStateCheck(t *testing.T) {
	cfg := config.Sample()
	if err := stateTableProblems(cfg, liveTable(cfg)); err != nil {
		t.Errorf("a correctly provisioned team failed the check: %v", err)
	}
}

// The check this replaces asserted only that the team had *some* states,
// which is true of every Linear team ever created.
func TestAnEmptyTeamSaysRunSetupRatherThanListingEveryState(t *testing.T) {
	cfg := config.Sample()
	err := stateTableProblems(cfg, nil)
	if err == nil {
		t.Fatal("a team with no states passed")
	}
	if !strings.Contains(err.Error(), "no states at all") {
		t.Errorf("an unprovisioned team was reported as N missing states: %v", err)
	}
}

// A state the config maps and the team does not carry. `plane.Build`
// also refuses this, one at a time and at the end of the run; here it
// arrives first and all at once.
func TestMissingStatesAreAllNamedAtOnce(t *testing.T) {
	cfg := config.Sample()
	live := liveTable(cfg)
	// Drop two.
	var trimmed []tracker.StateInfo
	for _, s := range live {
		if s.Name == cfg.StateName(protocol.Designing) || s.Name == cfg.StateName(protocol.Checks) {
			continue
		}
		trimmed = append(trimmed, s)
	}
	err := stateTableProblems(cfg, trimmed)
	if err == nil {
		t.Fatal("two missing states passed the check")
	}
	for _, want := range []string{cfg.StateName(protocol.Designing), cfg.StateName(protocol.Checks)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing from the report, so fixing one and re-running is the only way to find the other:\n%v", want, err)
		}
	}
}

// The half nothing else checks at runtime. `setup` compares categories
// and then refuses to retype a live state, so a category that drifted
// stays drifted: the name still resolves, the pipeline still writes to
// it, and the category is what decides which states are triage.
func TestACategoryMismatchIsReported(t *testing.T) {
	cfg := config.Sample()
	live := liveTable(cfg)
	for i := range live {
		if live[i].Name == cfg.StateName(protocol.Todo) {
			live[i].Category = protocol.CategoryCompleted
		}
	}
	err := stateTableProblems(cfg, live)
	if err == nil {
		t.Fatal("a state in the wrong category passed — nothing else catches this")
	}
	if !strings.Contains(err.Error(), "category") {
		t.Errorf("the mismatch was not described as a category problem: %v", err)
	}
	if !strings.Contains(err.Error(), "resolve in Linear") {
		t.Errorf("the remedy is wrong: setup will not retype a live state, so `setup --apply` is not the fix here:\n%v", err)
	}
}
