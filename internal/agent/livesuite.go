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
	switch result {
	case "pass", "fail", "no-tests":
	default:
		return fmt.Errorf("live-suite report: result must be pass, fail or no-tests, got %q", result)
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
	switch result {
	case "fail":
		prose = "Live suite FAILED. Read the linked run; anything real becomes a blocker filed against this milestone and marked blocking this ticket (DESIGN §10)."
	case "no-tests":
		// Neither verdict, and saying so is the whole point. A project
		// with no live tests exits the runner non-zero — an `--only`
		// filter matching nothing is an error, not an empty pass — so
		// this arrived as `fail` and read as a broken suite. Every
		// boundary on every project starts here, which means the one
		// automated signal the boundary protocol has was red for a
		// structural reason on its first outing and cost a real
		// investigation. Reporting it as a pass would be worse: nothing
		// was checked, and "a live check that never runs is how merged
		// and green quietly diverges from works against the world"
		// (DESIGN §10) is the reason this gate exists at all.
		prose = "Live suite ran NO tests — this project has none tagged for it yet, so nothing was checked against the real world.\n\nNot a failure and not a pass: no live coverage existed to run. The author's pass decides whether this milestone can close without it (DESIGN §10). Writing the first one is ordinary ticket work."
	}
	return p.CommentTicket(ctx, t.ID, m.Comment(prose))
}
