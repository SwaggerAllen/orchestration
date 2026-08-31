package plane

import (
	"context"
	"fmt"
	"os"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/deploy"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// attachDeployFacts gives each Merged ticket its deploy verdict: the merge
// SHA comes from the ticket's merged marker, the platform state from the
// deploy port, and the `>=` comparison is ancestry through the code host
// (DESIGN §13). A Merged ticket with no marker, or no port to ask, stays
// pending — and pending is what the deploy timeout exists to catch, so
// missing facts degrade to Blocked-with-a-reason instead of silence.
func (p *Plane) attachDeployFacts(ctx context.Context, tickets []*core.Ticket) error {
	if p.Deploy == nil || p.Host == nil {
		for _, t := range tickets {
			if t.State == protocol.Merged && !t.IsBoundary() {
				t.Deploy = core.DeployPending
			}
		}
		return nil
	}

	var state *deploy.State
	asked := false
	for _, t := range tickets {
		if t.State != protocol.Merged || t.IsBoundary() {
			continue
		}
		mergeSHA := mergedSHA(t)
		if mergeSHA == "" {
			t.Deploy = core.DeployPending
			continue
		}
		if !asked {
			asked = true
			var err error
			if state, err = p.Deploy.State(ctx); err != nil {
				// A provider that answers with an error is the same
				// epistemic position as no provider at all, and that case
				// is already pending-with-a-timeout two branches up. This
				// used to abort the whole build, which made one
				// unreachable third-party GET fatal to *everything* the
				// sweep does — dispatch, CI routing, escalation, stale
				// claims — for as long as the provider stayed unreachable.
				// A deploy fact the plane cannot read must not cost it the
				// facts it can.
				fmt.Fprintln(os.Stderr, "pipeline: deploy detection unavailable; Merged tickets stay pending until the deploy timeout:", err)
				state = nil
			}
		}
		t.Deploy = core.DeployPending
		if state == nil {
			continue
		}
		if state.ActiveSHA != "" {
			deployed, err := p.Host.IsAncestor(ctx, mergeSHA, state.ActiveSHA)
			if err != nil {
				return err
			}
			if deployed {
				t.Deploy = core.DeployDeployed
				continue
			}
		}
		if state.Failed && state.FailedSHA != "" {
			inFailed, err := p.Host.IsAncestor(ctx, mergeSHA, state.FailedSHA)
			if err != nil {
				return err
			}
			if inFailed {
				t.Deploy = core.DeployFailed
			}
		}
	}
	return nil
}

// mergedSHA is the newest merged marker on the ticket. The deploy check
// compares the latest commit, unlike the retro note, which needs every
// one — that difference is the last line here rather than a second copy
// of the parse loop (core.MergedSHAs).
func mergedSHA(t *core.Ticket) string {
	shas := core.MergedSHAs(t)
	if len(shas) == 0 {
		return ""
	}
	return shas[len(shas)-1]
}
