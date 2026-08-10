package plane

import (
	"context"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/deploy"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

func TestDeployFactsRouteThroughAncestry(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	d := deploy.NewMemory()
	p.WithHost(h).WithDeploy(d)

	seedMerged := func(title, sha string) tracker.Issue {
		i := seedIssue(t, tr, cfg, title, protocol.Merged)
		m := marker.Marker{Kind: marker.Merged, Fields: map[string]string{"sha": sha}}
		if err := tr.CommentOnIssue(ctx, i.ID, m.Format()); err != nil {
			t.Fatal(err)
		}
		return i
	}

	deployed := seedMerged("Deployed", "m1")
	pending := seedMerged("Pending", "m2")
	failed := seedMerged("Failed", "m3")
	noMarker := seedIssue(t, tr, cfg, "No marker", protocol.Merged)

	d.Set(&deploy.State{ActiveSHA: "active", Failed: true, FailedSHA: "broken"}, nil)
	h.Ancestry["m1..active"] = true
	h.Ancestry["m3..broken"] = true
	// m2 is in neither: a merge that no finished build contains yet.

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]core.DeployStatus{}
	for _, tk := range snap.Tickets {
		got[tk.ID] = tk.Deploy
	}
	if got[deployed.ID] != core.DeployDeployed {
		t.Errorf("deployed ticket = %q", got[deployed.ID])
	}
	if got[pending.ID] != core.DeployPending {
		t.Errorf("pending ticket = %q", got[pending.ID])
	}
	if got[failed.ID] != core.DeployFailed {
		t.Errorf("failed ticket = %q", got[failed.ID])
	}
	if got[noMarker.ID] != core.DeployPending {
		t.Errorf("markerless ticket = %q — pending is what the timeout catches", got[noMarker.ID])
	}
}

func TestDeployFactsWithoutPortsStayPending(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	i := seedIssue(t, tr, cfg, "Merged, no infra", protocol.Merged)

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range snap.Tickets {
		if tk.ID == i.ID && tk.Deploy != core.DeployPending {
			t.Errorf("deploy = %q, want pending (the timeout is the honest backstop)", tk.Deploy)
		}
	}
	_ = cfg
}
