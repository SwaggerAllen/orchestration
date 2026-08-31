package plane

import (
	"context"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// The snapshot build is the only place that can find a hand merge: the
// PR is closed, so ListOpenPRs cannot see it, and the ticket carries no
// marker because reconcile never ran.
func TestBuildFindsAMergeThePipelineDidNotMake(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	p.WithHost(h)

	handMerged := seedIssue(t, tr, cfg, "Author merged this one", protocol.Merged)
	withMarker := seedIssue(t, tr, cfg, "Reconcile merged this one", protocol.Merged)
	m := marker.Marker{Kind: marker.Merged, Fields: map[string]string{"sha": "already"}}
	if err := tr.CommentOnIssue(ctx, withMarker.ID, m.Format()); err != nil {
		t.Fatal(err)
	}
	stillWorking := seedIssue(t, tr, cfg, "Not merged at all", protocol.InProgress)

	// Merged on the host, under each ticket's own branch.
	h.MergedPRs = []host.MergedPR{
		{Number: 42, Branch: lower(handMerged.Key) + "-author-merged-this-one", MergeSHA: "abc123"},
		{Number: 43, Branch: lower(withMarker.Key) + "-reconcile", MergeSHA: "def456"},
		{Number: 44, Branch: lower(stillWorking.Key) + "-wip", MergeSHA: "ghi789"},
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range snap.Tickets {
		switch tk.ID {
		case handMerged.ID:
			if len(tk.HandMerges) != 1 || tk.HandMerges[0].SHA != "abc123" || tk.HandMerges[0].PR != "42" {
				t.Errorf("hand-merged ticket got %+v, want one merge abc123/42", tk.HandMerges)
			}
		case withMarker.ID:
			if len(tk.HandMerges) != 0 {
				t.Errorf("a ticket that already carries its marker was looked up anyway: %+v", tk.HandMerges)
			}
		case stillWorking.ID:
			if len(tk.HandMerges) != 0 {
				t.Errorf("a ticket that is not in Merged was looked up: %+v", tk.HandMerges)
			}
		}
	}
}

func lower(s string) string {
	out := []byte(s)
	for i, b := range out {
		if b >= 'A' && b <= 'Z' {
			out[i] = b + 32
		}
	}
	return string(out)
}
