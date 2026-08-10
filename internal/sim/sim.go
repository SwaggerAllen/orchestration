// Package sim is the Ring-2 harness (PLAN §2): full pipeline lifecycles
// against in-memory fakes and a virtual clock, runnable with no accounts,
// no secrets, and no network. M0 ships the skeleton — scenario loading, the
// fake world, and the setup step — and M1 gives steps their meaning when
// the pure core exists to drive.
package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/deploy"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/setup"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// Scenario is one scripted lifecycle, loaded from a JSON file so the suite
// grows as a library of files rather than test code.
type Scenario struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
}

// Step is one scripted event. M0 defines the envelope only; kinds arrive
// with the pure core in M1 (seed-ticket, advance-clock, sweep, expect, ...).
type Step struct {
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"`
}

// World is the fake environment a scenario runs in. Tests reach into it to
// assert; steps mutate it. The clock is virtual: a scenario that needs a
// week passes in milliseconds (PLAN §1).
type World struct {
	Tracker *tracker.Memory
	Host    *host.Memory
	Deploy  *deploy.Memory
	Clock   time.Time
	Config  *config.Config
}

// Result is what a run reports.
type Result struct {
	Scenario string
	StepsRun int
}

// Load reads a scenario file.
func Load(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Scenario
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("%s: scenario has no name", path)
	}
	return &s, nil
}

// Run executes a scenario from nothing: fresh fakes, setup, then the steps.
// Every run asserts setup idempotence — the M0 gate is baked into the
// harness so no future scenario can regress it unnoticed.
func Run(ctx context.Context, sc *Scenario, cfg *config.Config) (*Result, error) {
	w := &World{
		Tracker: tracker.NewMemory(),
		Host:    host.NewMemory(),
		Deploy:  deploy.NewMemory(),
		// Fixed epoch: virtual time makes runs deterministic, and a
		// scenario that cares about time advances it with a step.
		Clock:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Config: cfg,
	}

	if _, err := setup.Run(ctx, w.Tracker, cfg, false); err != nil {
		return nil, fmt.Errorf("sim %q: setup: %w", sc.Name, err)
	}
	again, err := setup.Plan(ctx, w.Tracker, cfg)
	if err != nil {
		return nil, fmt.Errorf("sim %q: re-planning setup: %w", sc.Name, err)
	}
	if len(again) != 0 {
		return nil, fmt.Errorf("sim %q: setup is not idempotent, second plan wants %d actions", sc.Name, len(again))
	}

	for i, step := range sc.Steps {
		// M0 knows no step kinds. Failing loudly beats skipping: a scenario
		// written for a newer harness must not silently pass on an older one.
		return nil, fmt.Errorf("sim %q: step %d has kind %q, and the M0 harness implements no step kinds yet", sc.Name, i, step.Kind)
	}

	return &Result{Scenario: sc.Name, StepsRun: len(sc.Steps)}, nil
}
