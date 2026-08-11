package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/plane"
)

// LiveSuiteReport posts the live-suite result marker on the boundary
// ticket (DESIGN §10). The workflow that ran the suite reports its own
// verdict — programmatic comments come from the harness, never composed
// by a model (§9) — and the marker is what stops the sweep dispatching
// the suite again this milestone.
func LiveSuiteReport(ctx context.Context, p *plane.Plane, ticketKey, result, runURL string, now time.Time) error {
	if result != "pass" && result != "fail" {
		return fmt.Errorf("live-suite report: result must be pass or fail, got %q", result)
	}
	snap, err := p.Build(ctx, now, false)
	if err != nil {
		return err
	}
	var t *core.Ticket
	for _, cand := range snap.Tickets {
		if cand.Key == ticketKey {
			t = cand
		}
	}
	if t == nil {
		return fmt.Errorf("live-suite report: no ticket %q in the project scope", ticketKey)
	}
	if !t.IsBoundary() {
		return fmt.Errorf("live-suite report: %s is not a boundary ticket — the live suite reports only there (DESIGN §10)", ticketKey)
	}
	m := marker.Marker{Kind: marker.LiveSuite, Fields: map[string]string{
		"result": result,
		"run":    runURL,
	}}
	prose := "Live suite passed. The once-per-milestone end-to-end check is green."
	if result == "fail" {
		prose = "Live suite FAILED. Read the linked run; anything real becomes a blocker filed against this milestone and marked blocking this ticket (DESIGN §10)."
	}
	return p.CommentTicket(ctx, t.ID, m.Comment(prose))
}
