// Package plane connects the pure core to the ports: Build turns tracker
// state into a core.Snapshot, Execute turns core.Actions back into tracker
// writes. Everything between those two calls is the pure sweep, so this
// package holds all the translation and none of the rules.
package plane

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/deploy"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/state"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// Plane binds the ports and config for one project. Host and Deploy may
// be nil — a Linear-only plane builds snapshots with no run, CI or deploy
// facts, which the core reads as "awaiting dispatch" / "no verdict" /
// "pending" and plans no harm beyond the deploy timeout's honest backstop.
type Plane struct {
	Tracker tracker.Tracker
	Host    host.Host
	Deploy  deploy.Deploy
	State   state.Store
	Config  *config.Config

	// resolved lazily, once per Plane: tracker state id <-> protocol state.
	stateByID map[string]protocol.State
	idByState map[protocol.State]string
	// triageStates are the team's Linear intake states, which the
	// protocol deliberately does not map.
	triageStates map[string]bool
	// keyByID is filled by Build; Execute needs keys for dispatch inputs.
	keyByID map[string]string
	// stateOf is likewise filled by Build: the state each ticket was in
	// when the snapshot was taken, which is the `from` of any move this
	// pass records.
	stateOf map[string]protocol.State
}

func New(t tracker.Tracker, cfg *config.Config) *Plane {
	return &Plane{Tracker: t, Config: cfg}
}

// WithHost attaches the code host port.
func (p *Plane) WithHost(h host.Host) *Plane {
	p.Host = h
	return p
}

// KnownState tells the plane where a ticket stands without building a
// snapshot to find out.
//
// Recording a move records an *edge*, and the origin comes from the
// snapshot Build took. The agent's finish and abort steps are separate
// processes from the claim: they load claim.json and transition, with no
// Build behind them and therefore no origin — so the move went down
// half-recorded, and a later sweep judging that half-edge planned a
// revert to nowhere and died on it, taking the whole pass with it.
//
// The claim already knows: it recorded the state the run holds while it
// works. This hands that back rather than spending a snapshot to
// rediscover it.
func (p *Plane) KnownState(ticketID string, s protocol.State) {
	if p.stateOf == nil {
		p.stateOf = map[string]protocol.State{}
	}
	p.stateOf[ticketID] = s
}

// WithState attaches the pipeline's own record of its writes. Without
// it the snapshot carries no records, so nothing is judged — which is
// the safe direction, and is exactly what a project that has not been
// wired up yet should get.
func (p *Plane) WithState(st state.Store) *Plane {
	p.State = st
	return p
}

// WithDeploy attaches the deploy platform port.
func (p *Plane) WithDeploy(d deploy.Deploy) *Plane {
	p.Deploy = d
	return p
}

// resolveStates maps tracker state ids to protocol states through the
// config's name table. Every mapped state must exist in the team — a
// missing one means setup hasn't run, and sweeping an unprovisioned team
// would misread every ticket.
func (p *Plane) resolveStates(ctx context.Context) error {
	if p.stateByID != nil {
		return nil
	}
	states, err := p.Tracker.ListStates(ctx, p.Config.Tracker.TeamID)
	if err != nil {
		return err
	}
	byName := map[string]tracker.StateInfo{}
	for _, s := range states {
		byName[s.Name] = s
	}
	p.stateByID = map[string]protocol.State{}
	p.idByState = map[protocol.State]string{}
	// Triage is Linear's intake type, and the protocol table has no
	// entry for it on purpose: a proposal awaiting the author's accept
	// or decline is not a pipeline ticket and must never be swept. It is
	// recorded here so the unmapped-state guard below can tell "the
	// boundary agent just filed a finding" from "a protocol state was
	// renamed in Linear".
	p.triageStates = map[string]bool{}
	for _, s := range states {
		if s.Category == protocol.CategoryTriage {
			p.triageStates[s.ID] = true
		}
	}
	for _, ps := range protocol.AllStates {
		name := p.Config.StateName(ps)
		info, ok := byName[name]
		if !ok {
			return fmt.Errorf("plane: team has no state %q (for %s) — run `pipeline setup` first", name, ps)
		}
		p.stateByID[info.ID] = ps
		p.idByState[ps] = info.ID
	}
	return nil
}

// roleOf resolves a tracker user id to a pipeline role via config.Actors.
// Resolution order is fixed — controlplane first — so a solo workspace's
// shared author/controlplane identity resolves to controlplane
// deterministically: the sweep trusts its own credential rather than
// randomly enforcing author rules against it. Unknown ids are RoleOther:
// the writer matrix treats them as strangers, which is exactly what an
// unmapped identity is.
func (p *Plane) roleOf(actorID string) core.Role {
	for _, role := range []string{"controlplane", "author", "design", "dev", "reconcile", "boundary"} {
		for _, id := range p.Config.Actors[role] {
			if id == actorID {
				return core.Role(role)
			}
		}
	}
	return core.RoleOther
}

// Build assembles the snapshot. Runs, CI and deploy verdicts remain
// zero-valued until their adapters land (M3, M4) — the core treats their
// absence as "awaiting dispatch" / "no verdict", which plans no harm.
func (p *Plane) Build(ctx context.Context, now time.Time, killSwitch bool) (*core.Snapshot, error) {
	if err := p.resolveStates(ctx); err != nil {
		return nil, err
	}
	issues, err := p.Tracker.ListIssues(ctx, p.Config.Tracker.TeamID, p.Config.Tracker.ProjectID)
	if err != nil {
		return nil, err
	}
	milestones, err := p.Tracker.ListMilestones(ctx, p.Config.Tracker.ProjectID)
	if err != nil {
		return nil, err
	}

	tickets := make([]*core.Ticket, 0, len(issues))
	p.keyByID = map[string]string{}
	p.stateOf = map[string]protocol.State{}
	for _, i := range issues {
		st, ok := p.stateByID[i.StateID]
		if !ok {
			if p.triageStates[i.StateID] {
				// Not a pipeline ticket yet. The boundary agent files
				// its findings here (DESIGN §10) and the author accepts
				// or declines them; until then there is no state for the
				// sweep to reason about. Skipped rather than mapped,
				// because mapping it would make findings dispatchable.
				//
				// This was fatal, which meant the boundary agent broke
				// its own next snapshot by doing its job: it filed three
				// proposals and then died on the first one it read back,
				// after all three steps had already succeeded.
				continue
			}
			// A state outside the protocol's table (a leftover team
			// default) makes the ticket unreadable; failing loudly beats
			// sweeping around it as if it weren't there.
			return nil, fmt.Errorf("plane: issue %s is in a state the config doesn't map (state id %s)", i.Key, i.StateID)
		}
		p.keyByID[i.ID] = i.Key
		p.stateOf[i.ID] = st
		t := &core.Ticket{
			ID: i.ID, Key: i.Key, Title: i.Title, Description: i.Description, URL: i.URL,
			State: st, StateSince: i.StateSince, CreatedAt: i.CreatedAt,
			Labels: i.Labels, Priority: i.Priority, Milestone: i.Milestone,
			AssigneeID: i.AssigneeID,
			Blocks:     i.Blocks, BlockedBy: i.BlockedBy,
		}
		if i.LastChange != nil {
			from, okFrom := p.stateByID[i.LastChange.FromStateID]
			to, okTo := p.stateByID[i.LastChange.ToStateID]
			if okTo {
				tr := core.Transition{To: to, Actor: p.roleOf(i.LastChange.ActorID), At: i.LastChange.At}
				if okFrom {
					tr.From = from
				}
				t.Last = &tr
			}
		}
		for _, cm := range i.Comments {
			t.Comments = append(t.Comments, core.Comment{Body: cm.Body, Actor: p.roleOf(cm.ActorID), At: cm.CreatedAt})
		}
		tickets = append(tickets, t)
	}

	if err := p.attachHostFacts(ctx, tickets); err != nil {
		return nil, err
	}
	if err := p.attachDeployFacts(ctx, tickets); err != nil {
		return nil, err
	}

	// The first configured author id is the assignment target (DESIGN §3).
	authorID := ""
	if ids := p.Config.Actors["author"]; len(ids) > 0 {
		authorID = ids[0]
	}

	// The pipeline's own record of what it has written. A read failure
	// is fatal to the snapshot rather than tolerated: an empty map and
	// an unreachable store are indistinguishable downstream, and the
	// second one silently turns every §9 invariant off — which is the
	// exact failure this store was built to end.
	recorded := map[string]core.RecordedMove{}
	if p.State != nil {
		recorded, err = p.State.All(ctx)
		if err != nil {
			return nil, fmt.Errorf("snapshot: reading the pipeline's own state record: %w", err)
		}
	}

	return &core.Snapshot{
		Now:              now,
		AuthorID:         authorID,
		CurrentMilestone: currentMilestone(milestones, tickets),
		KillSwitch:       killSwitch,
		Recorded:         recorded,
		StaleClaimGrace:  p.Config.StaleClaimGrace.Duration(),
		DeployTimeout:    p.Config.Deploy.Timeout.Duration(),
		Tickets:          tickets,
	}, nil
}

// currentMilestone is the first milestone, in the tracker's order, that
// has issues and is not complete. Complete means every issue resolved AND
// the boundary ticket exists and is Done — the boundary closing is what
// resumes the queue, so a drained milestone whose boundary is open is
// still the current one (DESIGN §10).
func currentMilestone(milestones []tracker.Milestone, tickets []*core.Ticket) string {
	sorted := make([]tracker.Milestone, len(milestones))
	copy(sorted, milestones)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].SortOrder < sorted[j].SortOrder })

	for _, m := range sorted {
		var any bool
		var boundary *core.Ticket
		complete := true
		for _, t := range tickets {
			if t.Milestone != m.Name {
				continue
			}
			if t.IsBoundary() {
				boundary = t
				continue
			}
			any = true
			if !t.Resolved() {
				complete = false
			}
		}
		if !any && boundary == nil {
			continue
		}
		if !complete || boundary == nil || !boundary.Resolved() {
			return m.Name
		}
	}
	return ""
}
