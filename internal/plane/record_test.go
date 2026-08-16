package plane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/state"
)

// Every transition the pipeline makes has to be recorded under the role
// that made it, or the next sweep reads it as the author's and reverts
// it. The role is a parameter precisely so this cannot be forgotten —
// this asserts it arrives intact.
func TestATransitionRecordsItsEdgeAndRole(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	st := state.NewMemory()
	p.WithState(st)

	issue := seedIssue(t, tr, cfg, "Worker", protocol.ReadyForDev)
	if _, err := p.Build(ctx, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if err := p.TransitionTicket(ctx, issue.ID, protocol.InProgress, core.RoleDev); err != nil {
		t.Fatal(err)
	}

	all, err := st.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := all[issue.ID]
	if !ok {
		t.Fatal("the move was made without being recorded")
	}
	if rec.From != protocol.ReadyForDev || rec.To != protocol.InProgress {
		t.Errorf("edge = %s -> %s, want ready_for_dev -> in_progress", rec.From, rec.To)
	}
	if rec.Role != core.RoleDev {
		t.Errorf("role = %q, want dev — the writer matrix judges roles", rec.Role)
	}
}

// A second move in the same process records the edge it actually made,
// not the one the snapshot was built with.
func TestASecondTransitionRecordsTheEdgeItMade(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	st := state.NewMemory()
	p.WithState(st)

	issue := seedIssue(t, tr, cfg, "Worker", protocol.ReadyForDev)
	if _, err := p.Build(ctx, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if err := p.TransitionTicket(ctx, issue.ID, protocol.InProgress, core.RoleDev); err != nil {
		t.Fatal(err)
	}
	if err := p.TransitionTicket(ctx, issue.ID, protocol.Checks, core.RoleDev); err != nil {
		t.Fatal(err)
	}
	all, _ := st.All(ctx)
	if rec := all[issue.ID]; rec.From != protocol.InProgress || rec.To != protocol.Checks {
		t.Errorf("edge = %s -> %s, want in_progress -> checks", rec.From, rec.To)
	}
}

// Fail closed. An unrecordable move must not happen: a transition that
// lands without its record reads as a human's on the next sweep, gets
// reverted, re-made and reverted again. The sweep is convergent, so
// declining costs a beat and nothing else.
func TestAnUnrecordableMoveDoesNotHappen(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	st := state.NewMemory()
	st.FailWrites = errors.New("durable object unreachable")
	p.WithState(st)

	issue := seedIssue(t, tr, cfg, "Worker", protocol.ReadyForDev)
	if _, err := p.Build(ctx, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	err := p.TransitionTicket(ctx, issue.ID, protocol.InProgress, core.RoleDev)
	if err == nil {
		t.Fatal("an unrecordable move was made anyway")
	}
	if !strings.Contains(err.Error(), "recording the move before making it") {
		t.Errorf("the error does not say what failed: %v", err)
	}
	after, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range after.Tickets {
		if tk.ID == issue.ID && tk.State != protocol.ReadyForDev {
			t.Errorf("the ticket moved to %s despite the record failing", tk.State)
		}
	}
}

// An unreadable store must not read as an empty one. Empty means "no
// ticket has a record", which turns every invariant off — and that is
// the exact failure this store exists to end, so it has to be loud.
func TestAnUnreadableStoreFailsTheSnapshot(t *testing.T) {
	ctx := context.Background()
	_, _, p := world(t)
	st := state.NewMemory()
	st.FailReads = errors.New("durable object unreachable")
	p.WithState(st)

	if _, err := p.Build(ctx, time.Now(), false); err == nil {
		t.Fatal("an unreadable state store produced a snapshot")
	}
}
