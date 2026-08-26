package plane

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/state"
)

// dispatchWorld is a plane with a host and a move store, plus two
// tickets, built so Execute has its key table.
func dispatchWorld(t *testing.T) (*host.Memory, *Plane, string, string) {
	t.Helper()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	p.WithHost(h)
	a := seedIssue(t, tr, cfg, "First", protocol.ReadyForDev)
	b := seedIssue(t, tr, cfg, "Second", protocol.ReadyForDev)
	if _, err := p.Build(context.Background(), time.Now(), false); err != nil {
		t.Fatal(err)
	}
	return h, p, a.ID, b.ID
}

func dispatch(ticketID string, kind core.AgentKind) core.Action {
	return core.Action{Kind: core.ActDispatch, TicketID: ticketID, Agent: kind}
}

// The race this closes: a run does not appear in the agent-run list the
// instant it is dispatched, so a sweep landing in that window sees an
// idle agent and dispatches again. Two boundary agents ran Catapult's
// ORC-45 to completion that way.
//
// core.VerifyPickup aborts the loser in seconds; this stops the second
// dispatch from ever being made.
func TestDispatchReservesTheKindAndSkipsASecondTicket(t *testing.T) {
	ctx := context.Background()
	h, p, a, b := dispatchWorld(t)

	var log bytes.Buffer
	if err := p.Execute(ctx, []core.Action{dispatch(a, core.AgentDev), dispatch(b, core.AgentDev)}, &log); err != nil {
		t.Fatal(err)
	}
	if len(h.Dispatches) != 1 {
		t.Fatalf("dispatched %d runs of one agent kind, want 1", len(h.Dispatches))
	}
	if !strings.Contains(log.String(), "dispatch skipped") {
		t.Errorf("the skipped dispatch was silent, so a stalled queue looks like a stopped pipeline:\n%s", log.String())
	}
}

// A different kind is a different lock. Serialising every agent behind
// one reservation would be the mutex failing in the other direction.
func TestDispatchReservationIsPerAgentKind(t *testing.T) {
	ctx := context.Background()
	h, p, a, b := dispatchWorld(t)

	var log bytes.Buffer
	if err := p.Execute(ctx, []core.Action{dispatch(a, core.AgentDev), dispatch(b, core.AgentDesign)}, &log); err != nil {
		t.Fatal(err)
	}
	if len(h.Dispatches) != 2 {
		t.Fatalf("dispatched %d runs across two agent kinds, want 2", len(h.Dispatches))
	}
}

// Re-dispatching the same ticket and kind is granted, not refused. The
// sweep re-plans every beat, and a ticket blocked by its own reservation
// would sit until the TTL expired.
func TestATicketIsNotBlockedByItsOwnReservation(t *testing.T) {
	ctx := context.Background()
	h, p, a, _ := dispatchWorld(t)

	var log bytes.Buffer
	for i := 0; i < 2; i++ {
		if err := p.Execute(ctx, []core.Action{dispatch(a, core.AgentDev)}, &log); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.Dispatches) != 2 {
		t.Fatalf("the same ticket dispatched %d times, want 2 — it was blocked by its own reservation", len(h.Dispatches))
	}
}

// A dispatch that never happened must not hold the kind. Otherwise one
// transient host error idles an agent for the whole TTL, which from the
// outside is indistinguishable from a pipeline that has stopped.
func TestAFailedDispatchHandsTheReservationBack(t *testing.T) {
	ctx := context.Background()
	h, p, a, b := dispatchWorld(t)
	h.DispatchErr = errors.New("workflow_dispatch: HTTP 502")

	var log bytes.Buffer
	if err := p.Execute(ctx, []core.Action{dispatch(a, core.AgentDev)}, &log); err == nil {
		t.Fatal("a failing dispatch was reported as success")
	}
	h.DispatchErr = nil

	// The next beat, on a different ticket: it must not be waiting out
	// the failed one's reservation.
	if err := p.Execute(ctx, []core.Action{dispatch(b, core.AgentDev)}, &log); err != nil {
		t.Fatal(err)
	}
	if len(h.Dispatches) != 1 {
		t.Fatalf("dispatched %d runs after a failure, want 1 — the reservation was not handed back", len(h.Dispatches))
	}
}

// The agent-side half. A claim is past the window the reservation
// covers, so holding it any longer only idles the kind.
func TestAClaimReleasesTheReservation(t *testing.T) {
	ctx := context.Background()
	h, p, a, b := dispatchWorld(t)

	var log bytes.Buffer
	if err := p.Execute(ctx, []core.Action{dispatch(a, core.AgentDev)}, &log); err != nil {
		t.Fatal(err)
	}
	p.ReleaseDispatchReservation(ctx, core.AgentDev, a)

	if err := p.Execute(ctx, []core.Action{dispatch(b, core.AgentDev)}, &log); err != nil {
		t.Fatal(err)
	}
	if len(h.Dispatches) != 2 {
		t.Fatalf("dispatched %d runs, want 2 — the claim did not release the kind", len(h.Dispatches))
	}
}

// A loser releasing must not free the winner's lock. The store's release
// is holder-scoped, which is what makes it safe to call from a run that
// just failed VerifyPickup.
func TestReleasingFromTheLosingTicketLeavesTheWinnerHolding(t *testing.T) {
	ctx := context.Background()
	h, p, a, b := dispatchWorld(t)

	var log bytes.Buffer
	if err := p.Execute(ctx, []core.Action{dispatch(a, core.AgentDev)}, &log); err != nil {
		t.Fatal(err)
	}
	// b never got the reservation; releasing on its behalf is the
	// gesture a losing run makes.
	p.ReleaseDispatchReservation(ctx, core.AgentDev, b)

	if err := p.Execute(ctx, []core.Action{dispatch(b, core.AgentDev)}, &log); err != nil {
		t.Fatal(err)
	}
	if len(h.Dispatches) != 1 {
		t.Fatalf("dispatched %d runs, want 1 — a losing run freed the winner's lock", len(h.Dispatches))
	}
}

// A store that cannot be written is not a store that may be ignored: an
// unrecorded dispatch is one the next sweep cannot tell from a duplicate.
// The sweep is convergent, so declining costs a beat.
func TestAnUnwritableStoreStopsTheDispatch(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	p.WithHost(h)
	st := state.NewMemory()
	a := seedIssue(t, tr, cfg, "First", protocol.ReadyForDev).ID
	if _, err := p.Build(ctx, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	p.WithState(st)
	st.FailWrites = errors.New("the metronome is unreachable")

	var log bytes.Buffer
	if err := p.Execute(ctx, []core.Action{dispatch(a, core.AgentDev)}, &log); err == nil {
		t.Fatal("dispatched without recording the reservation")
	}
	if len(h.Dispatches) != 0 {
		t.Errorf("dispatched %d runs with an unwritable store, want 0", len(h.Dispatches))
	}
}

// A project with no move store dispatches as before. The §9 invariants
// are already off there and preflight says so; this is one more thing in
// that sentence rather than a new silence.
func TestNoStoreMeansNoReservationAndNoBlocking(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	p.WithHost(h)
	a := seedIssue(t, tr, cfg, "First", protocol.ReadyForDev).ID
	b := seedIssue(t, tr, cfg, "Second", protocol.ReadyForDev).ID
	if _, err := p.Build(ctx, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	p.State = nil

	var log bytes.Buffer
	if err := p.Execute(ctx, []core.Action{dispatch(a, core.AgentDev), dispatch(b, core.AgentDev)}, &log); err != nil {
		t.Fatal(err)
	}
	if len(h.Dispatches) != 2 {
		t.Fatalf("dispatched %d runs without a store, want 2", len(h.Dispatches))
	}
}
