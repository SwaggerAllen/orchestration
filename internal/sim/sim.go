// Package sim is the Ring-2 harness (PLAN §2): full pipeline lifecycles
// against an in-memory world and a virtual clock, runnable with no
// accounts, no secrets, and no network. Scenarios are JSON files — a
// growing library of lifecycles rather than test code — and scripted
// steps play the threads the sweep doesn't own: agents, the author, CI.
//
// Every "sweep" step runs the core to convergence and fails the scenario
// if it doesn't settle — oscillation is the one bug class a polled control
// plane must never have, so the harness checks for it on every sweep of
// every scenario rather than in one dedicated test.
package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/deploy"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/setup"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// convergeLimit bounds the sweep-apply loop. A correct sweep settles in a
// handful of iterations (a transition enabling a dispatch, at most a few
// deep); ten means oscillation.
const convergeLimit = 10

// Scenario is one scripted lifecycle.
type Scenario struct {
	Name  string            `json:"name"`
	Steps []json.RawMessage `json:"steps"`
}

// World is the fake environment. In sim, a ticket's ID is its Key, so
// scenario files reference tickets naturally. The boundary ticket created
// by the sweep gets the deterministic key "B-<milestone>" for the same
// reason.
type World struct {
	Tracker *tracker.Memory
	Host    *host.Memory
	Deploy  *deploy.Memory

	Clock            time.Time
	Config           *config.Config
	Tickets          []*core.Ticket
	CurrentMilestone string
	KillSwitch       bool

	nextRun int
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

// Run executes a scenario from nothing: fresh fakes, provisioning, then
// the steps. Setup idempotence — the M0 gate — is asserted on every run so
// no future scenario can regress it unnoticed.
func Run(ctx context.Context, sc *Scenario, cfg *config.Config) (*Result, error) {
	w := &World{
		Tracker: tracker.NewMemory(),
		Host:    host.NewMemory(),
		Deploy:  deploy.NewMemory(),
		Clock:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Config:  cfg,
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

	for i, raw := range sc.Steps {
		if err := w.step(raw); err != nil {
			return nil, fmt.Errorf("sim %q: step %d: %w", sc.Name, i+1, err)
		}
	}
	return &Result{Scenario: sc.Name, StepsRun: len(sc.Steps)}, nil
}

func (w *World) snapshot() *core.Snapshot {
	return &core.Snapshot{
		Now:              w.Clock,
		CurrentMilestone: w.CurrentMilestone,
		KillSwitch:       w.KillSwitch,
		StaleClaimGrace:  w.Config.StaleClaimGrace.Duration(),
		DeployTimeout:    w.Config.Deploy.Timeout.Duration(),
		Tickets:          w.Tickets,
	}
}

func (w *World) ticket(key string) (*core.Ticket, error) {
	for _, t := range w.Tickets {
		if t.Key == key {
			return t, nil
		}
	}
	return nil, fmt.Errorf("no ticket %q", key)
}

// apply executes sweep actions against the world, playing the adapter's
// part: control-plane transitions stamp the control-plane role, dispatches
// start live runs, the boundary ticket appears fully formed.
func (w *World) apply(acts []core.Action) error {
	for _, a := range acts {
		switch a.Kind {
		case core.ActTransition:
			t, err := w.ticket(a.TicketID)
			if err != nil {
				return err
			}
			t.Last = &core.Transition{From: t.State, To: a.To, Actor: core.RoleControlPlane, At: w.Clock}
			t.State = a.To
			t.StateSince = w.Clock
			if a.Marker != nil {
				t.Comments = append(t.Comments, core.Comment{Body: a.Marker.Comment(a.Prose), Actor: core.RoleControlPlane, At: w.Clock})
			} else if a.Prose != "" {
				t.Comments = append(t.Comments, core.Comment{Body: a.Prose, Actor: core.RoleControlPlane, At: w.Clock})
			}
		case core.ActComment:
			t, err := w.ticket(a.TicketID)
			if err != nil {
				return err
			}
			t.Comments = append(t.Comments, core.Comment{Body: a.Marker.Comment(a.Prose), Actor: core.RoleControlPlane, At: w.Clock})
		case core.ActRemoveLabel:
			t, err := w.ticket(a.TicketID)
			if err != nil {
				return err
			}
			var keep []string
			for _, l := range t.Labels {
				if l != a.Label {
					keep = append(keep, l)
				}
			}
			t.Labels = keep
		case core.ActDispatch:
			t, err := w.ticket(a.TicketID)
			if err != nil {
				return err
			}
			w.nextRun++
			t.Run = &core.Run{ID: fmt.Sprintf("run_%d", w.nextRun), Kind: a.Agent, Live: true}
		case core.ActCreateBoundary:
			key := "B-" + a.Milestone
			b := &core.Ticket{
				ID: key, Key: key,
				Title:      "Milestone boundary — " + a.Milestone,
				State:      protocol.Todo,
				StateSince: w.Clock,
				CreatedAt:  w.Clock,
				Labels:     []string{core.LabelBoundary},
				Milestone:  a.Milestone,
			}
			if a.Prose != "" {
				b.Comments = append(b.Comments, core.Comment{Body: a.Prose, Actor: core.RoleControlPlane, At: w.Clock})
			}
			w.Tickets = append(w.Tickets, b)
		default:
			return fmt.Errorf("apply: unknown action kind %q", a.Kind)
		}
	}
	return nil
}

// converge sweeps and applies until the sweep plans nothing, failing on
// oscillation.
func (w *World) converge() error {
	for i := 0; i < convergeLimit; i++ {
		acts := core.Sweep(w.snapshot())
		if len(acts) == 0 {
			return nil
		}
		if err := w.apply(acts); err != nil {
			return err
		}
	}
	return fmt.Errorf("sweep did not converge in %d iterations — the control plane is oscillating", convergeLimit)
}
