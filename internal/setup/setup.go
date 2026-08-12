// Package setup provisions a Linear team with the pipeline's states and
// labels. The same command sets up the scratch team and, later, real
// projects — the dummy path IS the production path (PLAN §2), which is the
// point of having a command instead of a checklist.
//
// Setup only creates. It never renames, retypes or deletes: a state that
// already exists may hold tickets, and whether it is drift or data is a
// judgment for the author, not this command. Conflicts are reported as
// errors instead.
package setup

import (
	"context"
	"fmt"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// Op is the kind of a planned action.
type Op string

const (
	CreateState Op = "create-state"
	CreateLabel Op = "create-label"
)

// Action is one planned creation.
type Action struct {
	Op       Op
	Name     string
	Category protocol.Category // states only
	Color    string            // states only; cosmetic (protocol.Colors)
}

func (a Action) String() string {
	if a.Op == CreateState {
		return fmt.Sprintf("%s %q (%s, %s)", a.Op, a.Name, a.Category, a.Color)
	}
	return fmt.Sprintf("%s %q", a.Op, a.Name)
}

// Plan diffs what the team has against what the protocol requires and
// returns the creations that would close the gap. A second Plan after Apply
// returns nothing — idempotence is the M0 gate, and the sim harness asserts
// it on every run.
func Plan(ctx context.Context, t tracker.Tracker, cfg *config.Config) ([]Action, error) {
	teamID := cfg.Tracker.TeamID

	existing, err := t.ListStates(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("setup: listing states: %w", err)
	}
	byName := map[string]tracker.StateInfo{}
	for _, s := range existing {
		byName[s.Name] = s
	}

	var actions []Action
	for _, ps := range protocol.AllStates {
		name := cfg.StateName(ps)
		want := protocol.Categories[ps]
		have, ok := byName[name]
		if !ok {
			actions = append(actions, Action{Op: CreateState, Name: name, Category: want, Color: protocol.Colors[ps]})
			continue
		}
		if have.Category != want {
			// Retyping a live state silently could re-categorize existing
			// tickets under Linear's own automations; the author resolves it.
			return nil, fmt.Errorf(
				"setup: state %q exists with category %q, protocol wants %q — resolve in Linear, setup will not retype a live state",
				name, have.Category, want)
		}
	}

	labels, err := t.ListLabels(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("setup: listing labels: %w", err)
	}
	// Folded case, because a label that differs only in case is not a
	// second label anyone means to have: it is one taxonomy split across
	// two picker entries, and half the tickets end up on the wrong side of
	// it. Setup will not rename, so it reports and stops.
	haveLabel := map[string]string{}
	for _, l := range labels {
		haveLabel[strings.ToLower(l.Name)] = l.Name
	}
	for _, name := range protocol.Labels {
		switch have, ok := haveLabel[strings.ToLower(name)]; {
		case !ok:
			actions = append(actions, Action{Op: CreateLabel, Name: name})
		case have != name:
			return nil, fmt.Errorf(
				"setup: the tracker has label %q where the protocol wants %q — rename it (or delete it if unused) and run setup again; setup will not create a near-duplicate",
				have, name)
		}
	}
	return actions, nil
}

// Apply executes a plan.
func Apply(ctx context.Context, t tracker.Tracker, cfg *config.Config, actions []Action) error {
	teamID := cfg.Tracker.TeamID
	for _, a := range actions {
		var err error
		switch a.Op {
		case CreateState:
			_, err = t.CreateState(ctx, teamID, tracker.NewState{Name: a.Name, Category: a.Category, Color: a.Color})
		case CreateLabel:
			_, err = t.CreateLabel(ctx, teamID, a.Name)
		default:
			err = fmt.Errorf("unknown op %q", a.Op)
		}
		if err != nil {
			return fmt.Errorf("setup: %s: %w", a, err)
		}
	}
	return nil
}

// Run plans and, unless dryRun, applies. The returned actions are what was
// (or would be) done — the CLI prints them either way, because a mutating
// command that doesn't say what it did can't be audited.
func Run(ctx context.Context, t tracker.Tracker, cfg *config.Config, dryRun bool) ([]Action, error) {
	actions, err := Plan(ctx, t, cfg)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return actions, nil
	}
	if err := Apply(ctx, t, cfg, actions); err != nil {
		return actions, err
	}
	return actions, nil
}
