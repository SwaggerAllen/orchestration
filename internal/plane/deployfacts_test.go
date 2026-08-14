package plane

import (
	"context"
	"errors"
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

// An unreachable deploy provider must not cost the sweep the facts it can
// read. This was a live outage: one 404 from the App Platform deployments
// API failed every hourly sweep for twelve hours, so dispatch, CI routing
// and escalation all stopped — over a deploy that had in fact succeeded.
func TestDeployPortErrorLeavesTicketsPendingRatherThanFailingTheBuild(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	d := deploy.NewMemory()
	p.WithHost(h).WithDeploy(d)

	i := seedIssue(t, tr, cfg, "Merged, provider down", protocol.Merged)
	m := marker.Marker{Kind: marker.Merged, Fields: map[string]string{"sha": "m1"}}
	if err := tr.CommentOnIssue(ctx, i.ID, m.Format()); err != nil {
		t.Fatal(err)
	}
	d.Set(nil, errors.New("digitalocean: HTTP 404 from deployments API"))

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatalf("a deploy provider error must not fail the build: %v", err)
	}
	for _, tk := range snap.Tickets {
		if tk.ID == i.ID && tk.Deploy != core.DeployPending {
			t.Errorf("deploy = %q, want pending (the timeout is the honest backstop)", tk.Deploy)
		}
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
