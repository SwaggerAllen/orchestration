package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	if err := core.VerifyPickup(snap, t.ID, core.AgentReconcile, dispatchID); err != nil {
		return nil, err
	}
	// Past the window the dispatch reservation covers (DESIGN §6): this
	// run is claiming, so it is visible in the run list and the ordinary
	// singularity guard has it from here. Held any longer it only idles
	// the kind. After VerifyPickup, never before — a run that just lost
	// the race must not hand back the winner's lock, and the store's
	// release is holder-scoped so this one cannot.
	p.ReleaseDispatchReservation(ctx, core.AgentReconcile, t.ID)
	pr := p.PRForTicket(ctx, t.Key)
	if pr == nil {
		return nil, fmt.Errorf("reconcile claim %s: no open PR carries this ticket's key — nothing to verify", t.Key)
	}

	res := &ClaimResult{
		TicketID: t.ID, TicketKey: t.Key, Title: t.Title,
		Mode:        "reconcile",
		Role:        core.RoleReconcile,
		State:       t.State,
		Scope:       t.Description,
		Description: t.Description,
		Branch:      pr.Branch,
		PRNumber:    pr.Number,
		// Carried for two things. A re-evaluate flag among them means
		// another thread moved the ground under this ticket, and
		// reconciliation is the thread that owns the state it is
		// flagged in (DESIGN §7) — it cannot answer a question nobody
		// handed it. They also select the non-asks this pass reads
		// (§4); reconciliation runs late enough that the mutex labels
		// exist, so unlike design's claim this one selects on more than
		// the ticket's words.
		Labels: append([]string(nil), t.Labels...),
		// Reconciliation is the last gate before the merge (DESIGN §11),
		// so it is the last chance to catch a diff that landed
		// something the author had already refused.
		NonAsks: ClaimNonAsks(p.Config),
	}
	// Comments carry the deltas (DESIGN §2.3): reconciliation measures
	// the diff against the argument plus its accepted amendments.
	for _, c := range t.Comments {
		if prose, worth := marker.Prose(c.Body); worth {
			res.Comments = append(res.Comments, prose)
		}
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
	// Collision answers the re-evaluate flag: "holds" or "bites".
	// Required exactly when the ticket carries the flag, ignored
	// otherwise.
	//
	// Its own axis rather than a fourth outcome, because the two
	// questions are independent and collapsing them loses answers. The
	// outcome asks whether the diff says what the argument asked for;
	// this asks whether what another ticket did since invalidates it. A
	// diff can be a clean pass whose ground has moved, and it must not
	// merge; a diff can have drifted for reasons that have nothing to do
	// with the collision. One field cannot carry both without the model
	// choosing which answer to discard.
	Collision string `json:"collision,omitempty"`
}

// Collision verdicts.
const (
	CollisionHolds = protocol.CollisionHolds
	CollisionBites = protocol.CollisionBites
)

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
	if v.Collision != "" && !protocol.Known(protocol.CollisionVerdicts, v.Collision) {
		return nil, fmt.Errorf("verdict: collision %q is not one of %s", v.Collision, strings.Join(protocol.CollisionVerdicts, ", "))
	}
	switch v.Outcome {
	case "pass":
	case "fail", "cannot-tell":
		if strings.TrimSpace(v.Report) == "" {
			return nil, fmt.Errorf("verdict: outcome %q requires a report — on a fail it is the rework scope, on cannot-tell it is what a human must look at (DESIGN §11)", v.Outcome)
		}
	default:
		return nil, fmt.Errorf("verdict: outcome %q is not one of %s", v.Outcome, strings.Join(protocol.ReconcileOutcomes, ", "))
	}
	return &v, nil
}

// bounceUnmergeable returns a passing ticket to the queue because its
// branch conflicts with something that landed while it was in flight.
//
// The verdict stood: the work is right and the branch is stale, which
// are different problems with different owners. Before this, the merge
// error aborted the run and the abort sent the ticket to Blocked — and
// Blocked is the author's, exclusively, with no automatic way out
// (DESIGN §12). So the one failure in this pipeline that an agent is
// unambiguously equipped to fix was the one that always needed a human.
//
// Back to the queue rather than straight to Reworking: Reworking is the
// dev agent's own claim (DESIGN §3), and a state written by anyone else
// is reverted by the writer matrix (§9). The queue is how work is
// handed over here.
func bounceUnmergeable(ctx context.Context, p *plane.Plane, res *ClaimResult, v *Verdict) error {
	attempt := 1
	for _, c := range res.Comments {
		if m, ok, err := marker.Parse(c); err == nil && ok && m.Kind == marker.MergeConflict {
			attempt++
		}
	}
	m := marker.Marker{Kind: marker.MergeConflict, Fields: map[string]string{
		"pr":      fmt.Sprintf("%d", res.PRNumber),
		"attempt": fmt.Sprintf("%d", attempt),
	}}
	// This comment is the newest, so it is the scope (DESIGN §2.3), and
	// it has to say plainly that the work is not what is wrong — a dev
	// agent handed a bounce reads it as a finding about the diff unless
	// told otherwise, and would start re-litigating a design that
	// already passed.
	prose := fmt.Sprintf(`Reconciliation PASSED and the merge could not land: this branch conflicts with something that merged into main while the ticket was in flight.

**Nothing about the work is in question.** Do not revisit the design, the argument or the diff. The scope is exactly this: merge `+"`origin/main`"+` into the branch, resolve the conflicts, keep both sides' intent, run the quality gates, and finish. If a conflict cannot be resolved without changing what this ticket decided, that is a push-back rather than a guess (DESIGN §2.4).

The verdict that stood, for context:

%s`, v.Report)
	if err := p.CommentTicket(ctx, res.TicketID, m.Comment(prose)); err != nil {
		return err
	}
	return p.TransitionTicket(ctx, res.TicketID, protocol.ReadyForRework, core.RoleReconcile)
}

// FinishReconcile lands the three outcomes (DESIGN §11, §13):
//
//   - pass        -> merge, Merged, merged marker
//   - fail        -> bounce marker + report, Ready for rework
//   - cannot-tell -> merge, Merged + needs-review label; merging anyway is
//     deliberate — holding it would lock the screen mutex for weeks, and
//     "cannot tell" was never a finding of fault
func FinishReconcile(ctx context.Context, p *plane.Plane, h host.Host, res *ClaimResult, v *Verdict) error {
	if carriesLabel(res.Labels, core.LabelReEvaluate) {
		bounced, err := settleCollision(ctx, p, res, v)
		if err != nil || bounced {
			return err
		}
	}
	switch v.Outcome {
	case "fail":
		m := marker.Marker{Kind: marker.ReconcileBounce, Fields: map[string]string{"pr": fmt.Sprintf("%d", res.PRNumber)}}
		if err := p.CommentTicket(ctx, res.TicketID, m.Comment(v.Report)); err != nil {
			return err
		}
		// The bounce comment is now the newest comment, so it is the
		// scope the rework claim will serve (DESIGN §2.3). The sweep
		// escalates a second bounce to Blocked (DESIGN §12).
		return p.TransitionTicket(ctx, res.TicketID, protocol.ReadyForRework, core.RoleReconcile)

	case "pass", "cannot-tell":
		sha, err := h.MergePR(ctx, res.PRNumber)
		if errors.Is(err, host.ErrNotMergeable) {
			return bounceUnmergeable(ctx, p, res, v)
		}
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
		//
		// Never fatal, though. Past the merge there is no way back: the
		// PR is closed, so ClaimReconcile refuses ("no open PR carries
		// this ticket's key"), and the writer matrix reverts any hand
		// move to Merged or Done. An error returned here would abort the
		// run to Blocked and strand the ticket there permanently, with
		// its work already on main — which is exactly what happened to
		// ORC-1 on the first real merge this pipeline made.
		//
		// So it degrades instead: the ticket still reaches Merged, the
		// failure is on the ticket in words, and the deploy timeout
		// surfaces it as an ordinary Blocked later if nothing deploys.
		// A slow honest failure beats a fast unrecoverable one.
		if err := recordStandInDeploy(ctx, p, h, sha); err != nil {
			note := fmt.Sprintf("Merged, but recording the stand-in deployment failed: %v\n\n"+
				"The merge is done and this ticket is moving to Merged regardless — reconciliation cannot re-run "+
				"once the PR is closed, so failing here would strand it. If nothing records a deployment, the "+
				"post-deploy check has nothing to read and the deploy timeout will Block this ticket (DESIGN §12).", err)
			if cErr := p.CommentTicket(ctx, res.TicketID, note); cErr != nil {
				return fmt.Errorf("%w (and the note about it failed too: %v)", err, cErr)
			}
			fmt.Fprintln(os.Stderr, "warning: "+note)
		}
		return p.TransitionTicket(ctx, res.TicketID, protocol.Merged, core.RoleReconcile)
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

// settleCollision answers the re-evaluate flag before the verdict is
// applied, and reports whether it took the ticket away.
//
// Reconciliation owns the state the flag is sitting in, so it is the
// thread that clears it (DESIGN §7) — and it is the last thread before
// a merge, so if the collision bites this is the last chance to say so.
// The flag is cleared either way: the question has been answered, and a
// flag left behind would send the ticket to a design re-read that would
// evaluate the same collision a second time.
func settleCollision(ctx context.Context, p *plane.Plane, res *ClaimResult, v *Verdict) (bool, error) {
	// An unanswered collision bounces rather than merging. The two ways
	// to be wrong are not equal: a bounce costs a rework pass on work
	// that was fine, and a merge under a collision nobody judged is the
	// thing the flag exists to prevent, past recall the moment it lands.
	scope := ""
	switch v.Collision {
	case CollisionHolds:
	case CollisionBites:
		scope = v.Report
	default:
		scope = "The run did not answer whether the collision still matters, so this bounces rather than merging on an unjudged one."
	}
	if err := p.RemoveTicketLabel(ctx, res.TicketID, core.LabelReEvaluate); err != nil {
		return false, err
	}
	if scope == "" {
		return false, p.CommentTicket(ctx, res.TicketID,
			"**Re-evaluated: the collision does not invalidate this work.** Another thread flagged this ticket while it was in flight; reconciliation read the diff against the argument with that in view and the argument still holds. The flag is cleared and this proceeds on its own verdict (DESIGN §7).")
	}
	// The same marker the drift bounce uses, so the second-bounce
	// escalation counts them together (DESIGN §12). Two failures to land
	// one scope is a sequencing problem for the author whichever half
	// noticed it.
	m := marker.Marker{Kind: marker.ReconcileBounce, Fields: map[string]string{"pr": fmt.Sprintf("%d", res.PRNumber), "collision": "1"}}
	prose := "**Re-evaluated: the ground moved under this ticket.** Another thread changed something this work depends on, and reconciliation judged that it no longer says what the argument asked for.\n\n" +
		"This comment is the newest, so it is the scope (DESIGN §2.3). **The finding is not about your diff being wrong when it was written** — it is about what changed around it since:\n\n" + scope
	if err := p.CommentTicket(ctx, res.TicketID, m.Comment(prose)); err != nil {
		return false, err
	}
	return true, p.TransitionTicket(ctx, res.TicketID, protocol.ReadyForRework, core.RoleReconcile)
}

func carriesLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
