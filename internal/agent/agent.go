// Package agent is the run harness protocol: claim, finish, abort
// (DESIGN §6, §9; PLAN M3). It brackets the model invocation — the
// workflow runs `pipeline agent claim`, then Claude Code, then `pipeline
// agent finish` — so every protocol obligation is Go code under test and
// the model only ever does the work between.
package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	if m := baseRe.FindStringSubmatch(t.Description); m != nil {
		res.BaseSHA = m[1]
	}

	if pr := p.PRForTicket(ctx, t.Key); pr != nil {
		res.Branch = pr.Branch
		res.PRNumber = pr.Number
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

// Abort routes a run that cannot finish. Push-back goes to Designing with
// the argument — never a silently worse version (DESIGN §2.7). Failure
// goes to Blocked naming what failed (DESIGN §12).
func Abort(ctx context.Context, p *plane.Plane, ticketID, reason, message string) error {
	var to protocol.State
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
	default:
		return fmt.Errorf("abort: reason must be pushback or failed, got %q", reason)
	}
	if err := p.CommentTicket(ctx, ticketID, message); err != nil {
		return err
	}
	return p.TransitionTicket(ctx, ticketID, to)
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
