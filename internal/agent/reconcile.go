package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// ClaimReconcile is the reconcile agent's pickup. Unlike dev, the state
// transition already happened — the sweep wrote Reconciling on CI green
// (DESIGN §13) — so the claim is the expected-state assertion plus the
// dispatch marker, and finding the PR is mandatory: reconciliation without
// a diff has nothing to verify.
func ClaimReconcile(ctx context.Context, p *plane.Plane, ticketKey, dispatchID, dispatchURL string, now time.Time) (*ClaimResult, error) {
	snap, err := p.Build(ctx, now, false)
	if err != nil {
		return nil, err
	}
	var t *core.Ticket
	for _, cand := range snap.Tickets {
		if cand.Key == ticketKey {
			t = cand
		}
	}
	if t == nil {
		return nil, fmt.Errorf("reconcile claim: no ticket %q in the project scope", ticketKey)
	}
	if err := core.VerifyPickup(snap, t.ID, core.AgentReconcile); err != nil {
		return nil, err
	}
	pr := p.PRForTicket(ctx, t.Key)
	if pr == nil {
		return nil, fmt.Errorf("reconcile claim %s: no open PR carries this ticket's key — nothing to verify", t.Key)
	}

	res := &ClaimResult{
		TicketID: t.ID, TicketKey: t.Key, Title: t.Title,
		Mode:        "reconcile",
		Scope:       t.Description,
		Description: t.Description,
		Branch:      pr.Branch,
		PRNumber:    pr.Number,
	}
	// Comments carry the deltas (DESIGN §2.3): reconciliation measures
	// the diff against the argument plus its accepted amendments.
	for _, c := range t.Comments {
		res.Comments = append(res.Comments, c.Body)
	}

	m := marker.Marker{Kind: marker.Dispatch, Fields: map[string]string{
		"id":   dispatchID,
		"kind": "reconcile",
		"url":  dispatchURL,
	}}
	if err := p.CommentTicket(ctx, t.ID, m.Format()); err != nil {
		return nil, err
	}
	return res, nil
}

// Verdict is the reconcile model's structured output (DESIGN §11).
type Verdict struct {
	// Outcome: "pass", "fail", or "cannot-tell". Ambiguity must never
	// resolve itself as pass, so there is no default.
	Outcome string `json:"outcome"`
	// Report is the substance. Required for fail (the comment is the
	// rework scope — not optional) and for cannot-tell (what couldn't be
	// told). On a pass it carries the callouts, e.g. a surface changed
	// with no storybook variation (DESIGN §9).
	Report string `json:"report"`
}

// LoadVerdict reads and validates a verdict file.
func LoadVerdict(path string) (*Verdict, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("verdict: %w (a reconcile run that produced no verdict did not reconcile)", err)
	}
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("verdict %s: %w", path, err)
	}
	switch v.Outcome {
	case "pass":
	case "fail", "cannot-tell":
		if strings.TrimSpace(v.Report) == "" {
			return nil, fmt.Errorf("verdict: outcome %q requires a report — on a fail it is the rework scope, on cannot-tell it is what a human must look at (DESIGN §11)", v.Outcome)
		}
	default:
		return nil, fmt.Errorf("verdict: outcome %q is not pass, fail, or cannot-tell", v.Outcome)
	}
	return &v, nil
}

// FinishReconcile lands the three outcomes (DESIGN §11, §13):
//
//   - pass        -> merge, Merged, merged marker
//   - fail        -> bounce marker + report, Ready for rework
//   - cannot-tell -> merge, Merged + needs-review label; merging anyway is
//     deliberate — holding it would lock the screen mutex for weeks, and
//     "cannot tell" was never a finding of fault
func FinishReconcile(ctx context.Context, p *plane.Plane, h host.Host, res *ClaimResult, v *Verdict) error {
	switch v.Outcome {
	case "fail":
		m := marker.Marker{Kind: marker.ReconcileBounce, Fields: map[string]string{"pr": fmt.Sprintf("%d", res.PRNumber)}}
		if err := p.CommentTicket(ctx, res.TicketID, m.Comment(v.Report)); err != nil {
			return err
		}
		// The bounce comment is now the newest comment, so it is the
		// scope the rework claim will serve (DESIGN §2.3). The sweep
		// escalates a second bounce to Blocked (DESIGN §12).
		return p.TransitionTicket(ctx, res.TicketID, protocol.ReadyForRework)

	case "pass", "cannot-tell":
		sha, err := h.MergePR(ctx, res.PRNumber)
		if err != nil {
			return fmt.Errorf("reconcile %s: merging PR #%d: %w", res.TicketKey, res.PRNumber, err)
		}
		m := marker.Marker{Kind: marker.Merged, Fields: map[string]string{
			"sha": sha,
			"pr":  fmt.Sprintf("%d", res.PRNumber),
		}}
		prose := v.Report
		if v.Outcome == "cannot-tell" {
			if err := p.AddTicketLabel(ctx, res.TicketID, core.LabelNeedsReview); err != nil {
				return err
			}
			prose = "Reconciliation could not tell whether this landed as asked; it is merged and deployed, and a human look closes it (DESIGN §11).\n\n" + v.Report
		}
		if err := p.CommentTicket(ctx, res.TicketID, m.Comment(prose)); err != nil {
			return err
		}
		// Record the stand-in deployment before the ticket moves, so a
		// sweep arriving the instant it lands already has something to
		// read. Ordering matters more than it looks: the ticket entering
		// Merged is what starts the deploy timeout (DESIGN §12).
		if err := recordStandInDeploy(ctx, p, h, sha); err != nil {
			return err
		}
		return p.TransitionTicket(ctx, res.TicketID, protocol.Merged)
	}
	return fmt.Errorf("reconcile %s: unknown outcome %q", res.TicketKey, v.Outcome)
}

// recordStandInDeploy creates the deployment the post-deploy check will
// read, for projects whose platform is GitHub Deployments — the dummy's
// stand-in for a real host (PLAN M4). A real provider deploys itself and
// this does nothing: DigitalOcean rolls out on push and the sweep polls
// it, which is the path the stand-in exists to imitate.
//
// This was a project workflow on `push: main`, and it could never have
// worked. GitHub does not start a workflow run from an event created
// with GITHUB_TOKEN, and reconcile merges with exactly that, so no
// deployment was recorded for any agent merge — every ticket would have
// sat in Merged until the deploy timeout moved it to Blocked. Doing it
// here also puts the decision where the rest of the protocol lives,
// rather than in a trigger whose firing depends on who pushed.
func recordStandInDeploy(ctx context.Context, p *plane.Plane, h host.Host, sha string) error {
	if p.Config.Deploy.Provider != "github" {
		return nil
	}
	if err := h.RecordDeployment(ctx, sha, p.Config.Deploy.Endpoint); err != nil {
		return fmt.Errorf("recording the stand-in deployment for %s: %w", sha, err)
	}
	return nil
}
