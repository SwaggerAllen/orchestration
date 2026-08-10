package plane

import (
	"context"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/deploy"
	"github.com/SwaggerAllen/orchestration/internal/marker"
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
	for _, t := range tickets {
		if t.State != protocol.Merged || t.IsBoundary() {
			continue
		}
		mergeSHA := mergedSHA(t)
		if mergeSHA == "" {
			t.Deploy = core.DeployPending
			continue
		}
		if state == nil {
			var err error
			state, err = p.Deploy.State(ctx)
			if err != nil {
				return err
			}
		}
		t.Deploy = core.DeployPending
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

// mergedSHA reads the newest merged marker on the ticket.
func mergedSHA(t *core.Ticket) string {
	sha := ""
	for _, c := range t.Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok || m.Kind != marker.Merged {
			continue
		}
		if s := m.Fields["sha"]; s != "" {
			sha = s
		}
	}
	return sha
}
