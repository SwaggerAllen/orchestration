// Package agent is the run harness protocol: claim, finish, abort
// (DESIGN §6, §9; PLAN M3). It brackets the model invocation — the
// workflow runs `pipeline agent claim`, then Claude Code, then `pipeline
// agent finish` — so every protocol obligation is Go code under test and
// the model only ever does the work between.
package agent

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// baseRe extracts the `Base: <sha>` line from a description (DESIGN §4).
var baseRe = regexp.MustCompile(`(?m)^Base:\s*([0-9a-fA-F]{7,40})\s*$`)

// ClaimResult is everything the workflow steps after claim need.
type ClaimResult struct {
	TicketID  string
	TicketKey string
	Title     string
	// Mode is "dev" or "rework" — it decides where the scope came from
	// and which state was claimed.
	Mode string
	// State is the state the run holds while it works, recorded at claim
	// so an abort can say where the ticket is coming from. Only the
	// author moves a ticket out of Blocked and they choose the state
	// (DESIGN §12) — a choice that needs the origin, which the run knows
	// and the blocked ticket otherwise does not carry.
	State protocol.State
	// Scope is the text the agent implements: the description on a fresh
	// ticket, the newest comment on a returned one (DESIGN §2.3).
	Scope string
	// Description is always the original argument, for context.
	Description string
	// Branch is the existing PR's branch, or the derived name when no PR
	// exists yet (DESIGN §5: the branch name carries the issue key).
	Branch   string
	PRNumber int // 0 = no PR yet
	// BaseSHA is the recorded merge-base, "" if the description has none.
	// The base check flags rather than blocks (DESIGN §9): the harness
	// surfaces it, the agent judges it.
	BaseSHA string
	// Comments is the ticket's comment history, reconcile mode only —
	// comments carry the deltas the diff is measured against (DESIGN §2.3).
	Comments []string `json:",omitempty"`
	// NonAsks is the confirmed non-asks document (DESIGN §4), read out of
	// the project checkout at claim time. Design and boundary only: they
	// are the passes that propose.
	NonAsks *NonAsks `json:",omitempty"`
	// CIFailure is the failing build this rework is answering, read by
	// the harness at claim. Rework mode only, and absent when the branch
	// bounced for a reason other than red checks.
	CIFailure *CIFailure `json:",omitempty"`
}

// CIFailure is the evidence behind a rework. The failure comment is the
// scope (DESIGN §2.3), and until this existed that comment said "fix
// what the linked run reports" and gave a URL the agent could not open:
// it holds no GitHub credential, by design (DESIGN §9), and reading CI
// is not a reason to give it one. The harness reads and hands over the
// text, the same division as the ticket body and the non-asks document.
type CIFailure struct {
	RunURL string
	Jobs   []host.JobLog
	// Err is the read failure, if any. Stated rather than swallowed: a
	// rework that silently got no logs looks identical to a build with
	// nothing to say, and the agent would guess in exactly the case
	// where guessing is worst.
	Err string `json:",omitempty"`
}

// claimCIFailure fetches the failing jobs behind a rework. Failure to
// read them is recorded, never fatal — a rework run that could not fetch
// logs is still a rework run, and the alternative is refusing to work on
// a ticket because the evidence server was briefly unavailable.
func claimCIFailure(ctx context.Context, h host.Host, headSHA string) *CIFailure {
	if h == nil || headSHA == "" {
		return nil
	}
	checks, err := h.ChecksFor(ctx, headSHA)
	if err != nil {
		return &CIFailure{Err: err.Error()}
	}
	if checks.Status != host.ChecksRed {
		return nil
	}
	f := &CIFailure{RunURL: checks.RunURL}
	jobs, err := h.FailedJobLogs(ctx, headSHA)
	if err != nil {
		f.Err = err.Error()
		return f
	}
	f.Jobs = jobs
	return f
}

// NonAsks is the confirmed non-asks document as the claim found it.
// Three outcomes, all distinct and none collapsible: the file exists and
// here it is, the project records no non-asks, or the read failed. The
// last two look identical in an empty section, and they are not the same
// fact to an agent deciding whether it is about to contradict the author
// — so the prompt states which one happened, in as many words.
type NonAsks struct {
	// Path is project-relative, and it is what the design agent writes
	// back to: the doc is maintained alongside the screen and system
	// docs, in the same artifacts commit, reviewed in the same sign-off.
	Path  string
	Found bool
	Body  string
	// Err is the read failure, if any. A string rather than an error so
	// the claim file round-trips through JSON.
	Err string `json:",omitempty"`
}

// claimNonAsks reads the confirmed non-asks document for a pass that
// proposes. It lives in the project repo, which the calling job has
// already checked out — but cwd on a real run is the pipeline checkout,
// not the project, so the path resolves against the config's directory
// rather than against here.
//
// An unreadable file is not a reason to fail the claim — the pass still
// has work to do — so the failure travels to the prompt instead of
// ending the run.
func claimNonAsks(cfg *config.Config) *NonAsks {
	n := &NonAsks{Path: cfg.NonAsksPath}
	if n.Path == "" {
		return n
	}
	body, err := os.ReadFile(cfg.InRoot(n.Path))
	switch {
	case os.IsNotExist(err):
		return n // the project records none, which is an answer
	case err != nil:
		n.Err = err.Error()
		fmt.Fprintf(os.Stderr, "warning: could not read %s: %v\n", n.Path, err)
		return n
	}
	n.Found, n.Body = true, string(body)
	return n
}

// Claim performs pickup: assertions, state-transition-as-claim, and the
// dispatch marker (DESIGN §6). It refuses — without writing anything —
// unless the pickup assertions pass.
func Claim(ctx context.Context, p *plane.Plane, ticketKey, dispatchID, dispatchURL string, now time.Time) (*ClaimResult, error) {
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
		return nil, fmt.Errorf("claim: no ticket %q in the project scope", ticketKey)
	}
	if err := core.VerifyPickup(snap, t.ID, core.AgentDev); err != nil {
		return nil, err
	}

	res := &ClaimResult{
		TicketID: t.ID, TicketKey: t.Key, Title: t.Title,
		Description: t.Description,
	}
	claimState := protocol.InProgress
	res.Mode = "dev"
	res.Scope = t.Description
	if t.State == protocol.ReadyForRework {
		claimState = protocol.Reworking
		res.Mode = "rework"
		res.Scope = newestComment(t)
		if res.Scope == "" {
			// A rework ticket with no comments has no scope at all —
			// implementing the description re-lands merged work
			// (DESIGN §2.3), so refuse rather than guess.
			return nil, fmt.Errorf("claim %s: in rework but has no comments — the newest comment is the scope and there isn't one", t.Key)
		}
	}
	// The state the run works in, not the one it was picked up from: an
	// abort is coming from where the agent is, which is the state the
	// claim is about to write.
	res.State = claimState
	if m := baseRe.FindStringSubmatch(t.Description); m != nil {
		res.BaseSHA = m[1]
	}

	if pr := p.PRForTicket(ctx, t.Key); pr != nil {
		res.Branch = pr.Branch
		res.PRNumber = pr.Number
		if res.Mode == "rework" {
			res.CIFailure = claimCIFailure(ctx, p.Host, pr.HeadSHA)
		}
	} else {
		res.Branch = deriveBranch(t.Key, t.Title)
	}

	// State-transition-as-claim, then the dispatch marker: the id in a
	// comment is what makes the claim auditable and idempotency checkable
	// (DESIGN §6).
	if err := p.TransitionTicket(ctx, t.ID, claimState); err != nil {
		return nil, err
	}
	m := marker.Marker{Kind: marker.Dispatch, Fields: map[string]string{
		"id":   dispatchID,
		"kind": "dev",
		"url":  dispatchURL,
	}}
	if err := p.CommentTicket(ctx, t.ID, m.Format()); err != nil {
		return nil, err
	}
	return res, nil
}

// Finish completes a dev run: the hand-back comment, the PR flipped out
// of draft (or created), and the transition to Checks. An issue that
// moves without a hand-back is a state change nobody can audit
// (DESIGN vocabulary), so an empty hand-back is an error.
func Finish(ctx context.Context, p *plane.Plane, h host.Host, res *ClaimResult, handback string) error {
	if strings.TrimSpace(handback) == "" {
		return fmt.Errorf("finish %s: hand-back is empty — what landed, the commit, anything deliberately not done and why", res.TicketKey)
	}
	if err := p.CommentTicket(ctx, res.TicketID, handback); err != nil {
		return err
	}
	if res.PRNumber == 0 {
		pr, err := h.CreatePR(ctx, res.Branch,
			fmt.Sprintf("%s %s", res.TicketKey, res.Title),
			fmt.Sprintf("Implements %s. One PR per ticket; it stays open through rework (DESIGN §5).", res.TicketKey),
			false)
		if err != nil {
			return fmt.Errorf("finish %s: creating PR: %w", res.TicketKey, err)
		}
		res.PRNumber = pr.Number
	} else if err := h.MarkPRReady(ctx, res.PRNumber); err != nil {
		return fmt.Errorf("finish %s: undraft PR #%d: %w", res.TicketKey, res.PRNumber, err)
	}
	return p.TransitionTicket(ctx, res.TicketID, protocol.Checks)
}

// Abort routes a run that cannot finish. Three reasons, and the third is
// not a failure:
//
//   - pushback   -> Designing with the argument, never a silently worse
//     version (DESIGN §2.7)
//   - failed     -> Blocked naming what failed (DESIGN §12)
//   - needs-setup -> Blocked with the needs-setup label: nothing failed
//     and no judgment is owed, a human has to do something the run
//     cannot — put a secret in an environment, enable an API, create an
//     account. Without its own flavor it lands in Blocked looking like a
//     failure, and the Blocked column stops being readable as "what is
//     broken".
//
// Both Blocked routes stamp the state the run was working in. Only the
// author moves a ticket out of Blocked and they choose the state
// (DESIGN §12); a ticket parked mid-flight for a secret has no obvious
// destination the way a needs-review ticket does, so the origin is
// recorded rather than left to the tracker's history.
func Abort(ctx context.Context, p *plane.Plane, res *ClaimResult, reason, message string) error {
	var to protocol.State
	var m *marker.Marker
	switch reason {
	case "pushback":
		to = protocol.Designing
		if strings.TrimSpace(message) == "" {
			return fmt.Errorf("abort: push-back without its argument is one the next pass repeats (DESIGN §3)")
		}
	case "failed":
		to = protocol.Blocked
		if message == "" {
			message = "Agent run failed; see the workflow logs."
		}
		m = &marker.Marker{Kind: marker.Blocked, Fields: map[string]string{}}
	case "needs-setup":
		if strings.TrimSpace(message) == "" {
			return fmt.Errorf("abort: needs-setup without saying what has to be set up is a ticket nobody can unblock")
		}
		to = protocol.Blocked
		m = &marker.Marker{Kind: marker.Blocked, Fields: map[string]string{"setup": "1"}}
		if err := p.AddTicketLabel(ctx, res.TicketID, core.LabelNeedsSetup); err != nil {
			return err
		}
	default:
		return fmt.Errorf("abort: reason must be pushback, failed or needs-setup, got %q", reason)
	}
	if m != nil {
		m.Fields["from"] = string(res.State)
		message = m.Comment(message)
	}
	if err := p.CommentTicket(ctx, res.TicketID, message); err != nil {
		return err
	}
	return p.TransitionTicket(ctx, res.TicketID, to)
}

// newestComment returns the body of the most recent comment.
func newestComment(t *core.Ticket) string {
	var best string
	var bestAt time.Time
	for _, c := range t.Comments {
		if c.At.After(bestAt) || best == "" && bestAt.IsZero() {
			best, bestAt = c.Body, c.At
		}
	}
	return best
}

// deriveBranch builds the branch name for a ticket that has no PR yet:
// lowercase issue key plus a slug of the title, because the key in the
// branch is what routes every PR event back to the ticket (DESIGN §5).
func deriveBranch(key, title string) string {
	slug := strings.ToLower(title)
	slug = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		return strings.ToLower(key)
	}
	return strings.ToLower(key) + "-" + slug
}
