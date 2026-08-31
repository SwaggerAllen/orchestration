package plane

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

func TestPRMatchingIsKeyBoundarySafe(t *testing.T) {
	prs := []host.PR{
		{Number: 1, Branch: "pipe-12-cap-screen"},
		{Number: 2, Branch: "feature/PIPE-1-hello"},
	}
	if pr := prForTicket(prs, "PIPE-1"); pr == nil || pr.Number != 2 {
		t.Errorf("PIPE-1 matched %+v — pipe-12 must not swallow it", pr)
	}
	if pr := prForTicket(prs, "PIPE-12"); pr == nil || pr.Number != 1 {
		t.Errorf("PIPE-12 matched %+v", pr)
	}
	if pr := prForTicket(prs, "PIPE-2"); pr != nil {
		t.Errorf("PIPE-2 matched %+v, want none", pr)
	}
}

// The outcome has to survive the crossing from the port's vocabulary to
// the core's, and nothing else asserts that it does. The adapter test
// proves GitHub's conclusion is read; the core tests prove the rule acts
// on it; this is the link between them, and a probe that removed the
// mapping here left every other test passing.
func TestHostFactsCarryTheRunOutcomeAcross(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	p.WithHost(h)

	i := seedIssue(t, tr, cfg, "Cancelled design", protocol.Designing)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.Runs = []host.AgentRun{
		{ID: "20", Kind: "design", TicketKey: i.Key, Live: false, EndedAt: old,
			Outcome: host.OutcomeCancelled},
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, cand := range snap.Tickets {
		if cand.ID != i.ID {
			continue
		}
		if cand.Run == nil {
			t.Fatalf("no run on %s", i.Key)
		}
		if cand.Run.Outcome != core.OutcomeCancelled {
			t.Errorf("outcome = %q, want %q — the rule that parks the ticket reads this",
				cand.Run.Outcome, core.OutcomeCancelled)
		}
		return
	}
	t.Fatalf("ticket %s not in the snapshot", i.Key)
}

func TestHostFactsPopulateRunsAndChecks(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	p.WithHost(h)

	i := seedIssue(t, tr, cfg, "Cap screen", protocol.Checks)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	h.Runs = []host.AgentRun{
		// An older completed run must not shadow the newer crashed one:
		// stale-claim detection measures the newest death.
		{ID: "10", Kind: "dev", TicketKey: i.Key, Live: false, EndedAt: old},
		{ID: "11", Kind: "dev", TicketKey: i.Key, Live: false, EndedAt: old.Add(time.Hour)},
	}
	h.PRs = []host.PR{{Number: 3, Branch: strings.ToLower(i.Key) + "-cap-screen", HeadSHA: "sha3", Draft: false}}
	h.CheckState["sha3"] = host.Checks{Status: host.ChecksRed, RunURL: "https://gh/fail"}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	var tk *core.Ticket
	for _, cand := range snap.Tickets {
		if cand.ID == i.ID {
			tk = cand
		}
	}
	if tk == nil {
		t.Fatal("ticket missing from snapshot")
	}
	if tk.Run == nil || tk.Run.ID != "11" {
		t.Errorf("run = %+v, want the newest (id 11)", tk.Run)
	}
	if tk.CI.Status != core.CIRed || tk.CI.RunURL != "https://gh/fail" {
		t.Errorf("ci = %+v", tk.CI)
	}

	// Draft PRs are not in Checks' scope: gates run with the flag off.
	h.PRs[0].Draft = true
	snap, err = p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, cand := range snap.Tickets {
		if cand.ID == i.ID && cand.CI.Status != core.CINone {
			t.Errorf("draft PR produced a CI verdict: %+v", cand.CI)
		}
	}
}
