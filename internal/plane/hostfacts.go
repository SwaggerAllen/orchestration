package plane

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// attachHostFacts fills each ticket's Run and CI from the code host: the
// latest agent run correlated by run-name, and the PR's check verdict for
// tickets sitting in Checks. Host nil means no facts, which the core reads
// as "awaiting dispatch" / "no verdict" — safe in both directions.
//
// **Project-wide reads are fatal; per-ticket reads degrade.** The run
// list and the open-PR list describe the whole project, and a snapshot
// missing either is not a snapshot. A verdict or a merge state belongs
// to one ticket, and losing it costs exactly that ticket its facts —
// so it is reported and skipped rather than taking every other ticket
// down with it. That distinction is the whole of the rule; the reason
// it is written here is that it was not obeyed and the failure looked
// like something else entirely (see the verdict read below).
// runOutcome carries the port's vocabulary across to the core's, the
// same crossing hostfacts already makes for CIStatus. Two enums rather
// than one shared string because the core is host-agnostic by
// construction: the sim drives it with no GitHub anywhere, and a core
// that matched on GitHub's own conclusion strings would make every
// scenario speak them too.
//
// An unrecognised value maps to OutcomeUnknown rather than being passed
// through, so a widened port cannot reach a core rule that has not been
// taught what the new value means.
func runOutcome(o host.RunOutcome) core.RunOutcome {
	switch o {
	case host.OutcomeSucceeded:
		return core.OutcomeSucceeded
	case host.OutcomeFailed:
		return core.OutcomeFailed
	case host.OutcomeCancelled:
		return core.OutcomeCancelled
	}
	return core.OutcomeUnknown
}

func (p *Plane) attachHostFacts(ctx context.Context, tickets []*core.Ticket) error {
	if p.Host == nil {
		return nil
	}
	runs, err := p.Host.ListAgentRuns(ctx)
	if err != nil {
		return err
	}
	prs, err := p.Host.ListOpenPRs(ctx)
	if err != nil {
		return err
	}

	latestRun := map[string]host.AgentRun{}
	liveRuns := map[string][]core.Run{}
	for _, r := range runs {
		prev, seen := latestRun[r.TicketKey]
		// Prefer a live run; otherwise the most recently ended one. A
		// crashed run must not be shadowed by an older completed one, or
		// stale-claim detection measures the wrong death.
		if !seen || (r.Live && !prev.Live) || (r.Live == prev.Live && r.EndedAt.After(prev.EndedAt)) {
			latestRun[r.TicketKey] = r
		}
		// Kept unfolded as well, because the collapse above loses the
		// one fact the pickup assertion needs: that there is more than
		// one. Two boundary runs on ORC-45 each read the collapsed Run,
		// each recognised it as its own, and both scanned the tree.
		if r.Live {
			// No outcome here, deliberately. A live run has not
			// concluded, so the host has nothing to report about how it
			// ended — carrying the field would be one nothing can set
			// and nothing reads, and a line no probe can break is a line
			// no test is covering.
			liveRuns[r.TicketKey] = append(liveRuns[r.TicketKey],
				core.Run{ID: r.ID, Kind: core.AgentKind(r.Kind), Live: true, EndedAt: r.EndedAt})
		}
	}

	for _, t := range tickets {
		if r, ok := latestRun[t.Key]; ok {
			t.Run = &core.Run{ID: r.ID, Kind: core.AgentKind(r.Kind), Live: r.Live, EndedAt: r.EndedAt,
				Outcome: runOutcome(r.Outcome)}
		}
		t.LiveRuns = liveRuns[t.Key]
		pr := prForTicket(prs, t.Key)
		if pr == nil {
			continue
		}
		// Checks verdicts are fetched only where the sweep reads them —
		// tickets in Checks with the draft flag off (DESIGN §13) — to
		// keep the snapshot a bounded number of API calls.
		if t.State == protocol.Checks && !pr.Draft {
			checks, err := p.Host.ChecksFor(ctx, pr.HeadSHA)
			if err != nil {
				// This ticket's verdict is unreadable; every other
				// ticket's facts are not. Aborting here made one host
				// read fatal to the whole snapshot, and therefore to
				// every claim in the project — Catapult's ORC-7 died
				// building a snapshot over a 403 on ORC-5's CI, a ticket
				// it had no interest in, and sat 23 minutes in a state
				// that said an agent was working on it.
				//
				// Same rule attachDeployFacts already follows, and the
				// same sentence justifies it: a fact the plane cannot
				// read must not cost it the facts it can. Degrading
				// leaves this ticket with no verdict, which the core
				// reads as "not judged yet" and waits on — a stalled
				// ticket instead of a stalled project.
				fmt.Fprintf(os.Stderr, "pipeline: %s: CI verdict unreadable, leaving it unjudged: %v\n", t.Key, err)
				continue
			}
			switch checks.Status {
			case host.ChecksGreen:
				t.CI = core.CIInfo{Status: core.CIGreen, RunURL: checks.RunURL}
			case host.ChecksRed:
				t.CI = core.CIInfo{Status: core.CIRed, RunURL: checks.RunURL,
					RunAttempt: checks.RunAttempt, FailedJobs: checks.FailedJobs}
			case host.ChecksPending:
				t.CI = core.CIInfo{Status: core.CIPending}
			}
			// Asked for every Checks ticket, not only the ones with no
			// verdict. A green PR that conflicts is reconcile's bounce
			// and works already; a ticket with no verdict is this one's,
			// and the two are told apart by the core rather than by
			// which facts the snapshot bothered to gather — a snapshot
			// that answers different questions depending on what it
			// found is one the pure core cannot be tested against.
			ms, err := p.Host.MergeStateFor(ctx, pr.Number)
			if err != nil {
				// Per-ticket like the verdict above, and degraded the
				// same way. Unknown mergeability is already a state the
				// core handles — GitHub answers "not yet" the same way
				// it answers nothing, so the conflict rule is written
				// never to act on it.
				fmt.Fprintf(os.Stderr, "pipeline: %s: merge state unreadable, leaving it unknown: %v\n", t.Key, err)
				continue
			}
			t.CI.Mergeable = core.MergeState(ms)
			t.CI.PRNumber = pr.Number
		}
	}
	return nil
}

// prForTicket finds the open PR whose branch carries the ticket key
// (DESIGN §5: the branch name is the routing requirement). The match is
// case-insensitive and boundary-checked so PIPE-1 never matches a
// pipe-12 branch.
func prForTicket(prs []host.PR, key string) *host.PR {
	lk := strings.ToLower(key)
	for i := range prs {
		lb := strings.ToLower(prs[i].Branch)
		idx := 0
		for {
			j := strings.Index(lb[idx:], lk)
			if j < 0 {
				break
			}
			start := idx + j
			end := start + len(lk)
			beforeOK := start == 0 || !isAlnum(lb[start-1])
			afterOK := end == len(lb) || !isAlnum(lb[end])
			if beforeOK && afterOK {
				return &prs[i]
			}
			idx = end
		}
	}
	return nil
}

func isAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z'
}
