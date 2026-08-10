package plane

import (
	"context"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// attachHostFacts fills each ticket's Run and CI from the code host: the
// latest agent run correlated by run-name, and the PR's check verdict for
// tickets sitting in Checks. Host nil means no facts, which the core reads
// as "awaiting dispatch" / "no verdict" — safe in both directions.
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
	for _, r := range runs {
		prev, seen := latestRun[r.TicketKey]
		// Prefer a live run; otherwise the most recently ended one. A
		// crashed run must not be shadowed by an older completed one, or
		// stale-claim detection measures the wrong death.
		if !seen || (r.Live && !prev.Live) || (r.Live == prev.Live && r.EndedAt.After(prev.EndedAt)) {
			latestRun[r.TicketKey] = r
		}
	}

	for _, t := range tickets {
		if r, ok := latestRun[t.Key]; ok {
			t.Run = &core.Run{ID: r.ID, Kind: core.AgentKind(r.Kind), Live: r.Live, EndedAt: r.EndedAt}
		}
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
				return err
			}
			switch checks.Status {
			case host.ChecksGreen:
				t.CI = core.CIInfo{Status: core.CIGreen, RunURL: checks.RunURL}
			case host.ChecksRed:
				t.CI = core.CIInfo{Status: core.CIRed, RunURL: checks.RunURL}
			case host.ChecksPending:
				t.CI = core.CIInfo{Status: core.CIPending}
			}
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
