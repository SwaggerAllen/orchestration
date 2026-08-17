// Package agent is the run harness protocol: claim, finish, abort
// (DESIGN §6, §9; PLAN M3). It brackets the model invocation — the
// workflow runs `pipeline agent claim`, then Claude Code, then `pipeline
// agent finish` — so every protocol obligation is Go code under test and
// the model only ever does the work between.
package agent

import (
	"context"
	"encoding/json"
	"errors"
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

// baseSHA is the merge-base the design was drawn against: the newest
// base marker the harness posted, falling back to a `Base:` line an
// author wrote by hand. The description form is what DESIGN §4 documents
// and stays supported for exactly that reason — but nothing automated
// can write there, because the description is the immutable argument
// (§2.3), which is why the marker exists.
func baseSHA(t *core.Ticket) string {
	for i := len(t.Comments) - 1; i >= 0; i-- {
		m, ok, err := marker.Parse(t.Comments[i].Body)
		if err == nil && ok && m.Kind == marker.Base {
			if sha := m.Fields["sha"]; sha != "" {
				return sha
			}
		}
	}
	if m := baseRe.FindStringSubmatch(t.Description); m != nil {
		return m[1]
	}
	return ""
}

// ClaimResult is everything the workflow steps after claim need.
type ClaimResult struct {
	TicketID  string
	TicketKey string
	Title     string
	// Mode is "dev" or "rework" — it decides where the scope came from
	// and which state was claimed.
	Mode string
	// Role is which part of the pipeline this run is. It travels in
	// claim.json so the finish and abort steps — separate processes —
	// record their moves under the same role the claim did.
	Role core.Role
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
	// FailedJobLogs alone, deliberately. This used to ask ChecksFor
	// first and only fetch logs when it said red — a reasonable shape
	// that fetched the answer through the checks API, which needs the
	// "Checks" permission. A fine-grained PAT has no such permission to
	// grant, and agent runs act as exactly that token, so the gate in
	// front of the evidence was the thing that 403'd. Asking only for
	// failing jobs answers "was it red" and "what broke" in one call,
	// through actions: read, which the token does hold.
	//
	// It also narrows the section to when it is true: a rework bounced
	// by reconciliation rather than by CI now gets no failing-build
	// section at all, instead of one reporting that the harness could
	// not read logs for a build that never failed.
	jobs, err := h.FailedJobLogs(ctx, headSHA)
	if err != nil {
		return &CIFailure{Err: err.Error()}
	}
	if len(jobs) == 0 {
		return nil
	}
	return &CIFailure{RunURL: jobs[0].URL, Jobs: jobs}
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

// ClaimNonAsks reads the confirmed non-asks document for a pass that
// proposes. It lives in the project repo, which the calling job has
// already checked out — but cwd on a real run is the pipeline checkout,
// not the project, so the path resolves against the config's directory
// rather than against here.
//
// An unreadable file is not a reason to fail the claim — the pass still
// has work to do — so the failure travels to the prompt instead of
// ending the run.
func ClaimNonAsks(cfg *config.Config) *NonAsks {
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
	res.Role = core.RoleDev
	res.BaseSHA = baseSHA(t)

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
	if err := p.TransitionTicket(ctx, t.ID, claimState, core.RoleDev); err != nil {
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
//
// o is the model's own account of how the run ended; nil means "done".
// commits is how many commits the branch carries that main does not,
// counted by the caller because git is where that fact lives, with -1
// for "nobody measured". Between them they separate the three reasons a
// run legitimately changes nothing (DESIGN §12) from the case where it
// changed nothing and said nothing about why.
func Finish(ctx context.Context, p *plane.Plane, h host.Host, res *ClaimResult, handback string, commits int, o *DevOutcome) error {
	if strings.TrimSpace(handback) == "" {
		return fmt.Errorf("finish %s: hand-back is empty — what landed, the commit, anything deliberately not done and why", res.TicketKey)
	}
	if o == nil {
		o = &DevOutcome{Outcome: "done"}
	}
	// Unmeasured is only tolerable when a PR already exists, or when the
	// model named an outcome that opens none: those are the cases where
	// the count decides nothing. Otherwise it decides whether a PR can be
	// opened at all, and guessing wrong is how this used to fail — the
	// host rejected the empty PR, the step exited non-zero, and the
	// generic abort stamped Blocked/failed over a hand-back explaining
	// that the run had done exactly the right thing.
	if o.Outcome == "done" && res.PRNumber == 0 && commits < 0 {
		return fmt.Errorf("finish %s: no PR yet and nobody counted the commits, so this cannot tell work from a no-op — pass --commits \"$(git rev-list --count origin/main..HEAD)\"", res.TicketKey)
	}
	// Before the routing below, so the run's own account is on the ticket
	// whichever way this goes. On every route but "done" it is the more
	// useful half: the label says the shape of the problem and the
	// hand-back says what the run actually saw.
	if err := p.CommentTicket(ctx, res.TicketID, handback); err != nil {
		return err
	}
	if reason, named := devOutcomes[o.Outcome]; named {
		summary := o.Summary
		// A named outcome means the run changed nothing, so commits on the
		// branch contradict it. Reported rather than refused: the outcome
		// is the model's own account and overriding it would be the
		// harness guessing, while failing here would land the ticket in
		// Blocked/failed — the exact outcome these routes exist to avoid.
		// The commits are already pushed, so a human can look.
		if commits > 0 {
			summary += fmt.Sprintf("\n\n---\n\n_The run reported `%s` but left %d commit(s) on `%s`. Nothing was opened for review; the branch is there if they matter._", o.Outcome, commits, res.Branch)
		}
		return Abort(ctx, p, res, reason, summary)
	}
	// Nothing to open a PR with, and no account of why. Filed as
	// scope-satisfied like a run that said so itself: it is the same
	// status, wants the same look from the same person, and the comment
	// below is where the difference lives. A second label for it was
	// tried and dropped — two labels the author triages identically are
	// two labels they have to learn the difference between for nothing.
	if res.PRNumber == 0 && commits == 0 {
		return Abort(ctx, p, res, "scope-satisfied", unexplainedMessage)
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
	return p.TransitionTicket(ctx, res.TicketID, protocol.Checks, core.RoleDev)
}

// softLabel attaches a label without letting the attachment decide
// whether the abort happens.
//
// The plane provisions a missing label on demand, so what is left here is
// a tracker refusing outright: a permissions gap, an API error, a name
// already taken at another scope. On a hard failure any of those would
// abort the abort, fail the finish step, and let the generic handler
// stamp Blocked/failed on a run that did exactly the right thing — which
// is the outcome these routes exist to prevent, so it must not be
// reachable through them.
//
// The move is the load-bearing half; the label is how the column stays
// readable. Saying so on the ticket beats both failing and going quiet.
func softLabel(ctx context.Context, p *plane.Plane, res *ClaimResult, message, label string) string {
	if err := p.AddTicketLabel(ctx, res.TicketID, label); err != nil {
		return message + fmt.Sprintf("\n\n---\n\n_The `%s` label could not be attached (%v). The move itself is unaffected; run `pipeline setup` on this project to provision the label set._", label, err)
	}
	return message
}

// unexplainedMessage is what a zero-diff run leaves on the ticket when
// it named no outcome of its own.
//
// It lands under the same label as a run that said "scope-satisfied",
// because it is the same status and the same question for the same
// person. What it does not do is claim the run said so: the prose is
// where the harness admits it is inferring, which is the honest place
// for that — a label the author reads at a glance should not be the
// thing carrying a hedge.
//
// It is a park rather than a failure because nothing failed. This used
// to arrive as Blocked/failed — the host rejected a PR with no commits
// behind it, the step exited non-zero, and the generic abort stamped the
// ticket over a hand-back arguing, correctly, that the right answer was
// to change nothing. A Blocked column where that looks the same as a
// crash is a column that has stopped answering "what is broken".
const unexplainedMessage = `**No changes, and no reason given.** This run produced nothing main does not already have, and wrote no outcome saying why — so there is no diff to open a PR with, and the label above is an inference rather than the run's own word.

It is filed as scope-satisfied because that is overwhelmingly the usual cause and it is the same question either way, but take the label as a starting point rather than a finding. A run that changes nothing is supposed to say which of the three applies — the scope was already satisfied, something needs setting up first, or the design cannot be built as drawn — and this one said none of them, which is itself worth a look.

The hand-back above is the run's own account and the only thing here that looked at the actual repository. Read it, then pick: cancel the ticket if it duplicates merged work — ` + "`Canceled`" + `, not ` + "`Done`" + `, so ` + "`Done`" + ` stays a record of what actually shipped (DESIGN §2.6) — or send it back with a scope naming what is still missing.`

// DevOutcome is the dev model's structured output: which of the ways a
// run can end this one took, and the argument for it.
//
// A file rather than a command the model runs, for the same reason
// design and reconcile use files. The model's run carries the model
// credential and nothing else — no tracker key, no GitHub token — so it
// cannot transition a ticket, and that is the design rather than an
// oversight: the harness brackets the model, and every protocol
// obligation stays in Go under test. prompts/dev.md told the model to
// run `pipeline agent abort --reason pushback` for a long time, an
// instruction it was never once able to follow.
type DevOutcome struct {
	// Outcome: "done", "scope-satisfied", "needs-setup" or "pushback".
	// Absent file means "done" — an older prompt that never wrote one
	// still finishes, and a run that produced nothing without saying why
	// is caught by the commit count instead.
	Outcome string `json:"outcome"`
	// Summary is the argument. Required for everything but "done": each
	// of the other three ends with a human reading this and deciding
	// something, and one that arrives without it is a ticket nobody can
	// act on.
	Summary string `json:"summary"`
}

// devOutcomes maps each outcome to the abort reason that lands it.
// "done" is absent because it is the one that does not abort.
var devOutcomes = map[string]string{
	"scope-satisfied": "scope-satisfied",
	"needs-setup":     "needs-setup",
	"pushback":        "pushback",
}

// LoadDevOutcome reads the dev model's outcome. A missing file is not an
// error: it means "done", which is what almost every run is.
func LoadDevOutcome(path string) (*DevOutcome, error) {
	if path == "" {
		return &DevOutcome{Outcome: "done"}, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &DevOutcome{Outcome: "done"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dev outcome: %w", err)
	}
	var o DevOutcome
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("dev outcome %s: %w", path, err)
	}
	if o.Outcome == "" {
		o.Outcome = "done"
	}
	if _, ok := devOutcomes[o.Outcome]; !ok && o.Outcome != "done" {
		return nil, fmt.Errorf("dev outcome: %q is not one of done, scope-satisfied, needs-setup, pushback", o.Outcome)
	}
	if o.Outcome != "done" && strings.TrimSpace(o.Summary) == "" {
		return nil, fmt.Errorf("dev outcome: %q without its argument is a ticket nobody can act on — say what you found", o.Outcome)
	}
	return &o, nil
}

// Abort routes a run that cannot finish. Four reasons, and only
// "failed" is a failure:
//
//   - pushback   -> Blocked with the pushback label and the argument,
//     never a silently worse version (DESIGN §2.7)
//   - failed     -> Blocked naming what failed (DESIGN §12)
//   - needs-setup -> Blocked with the needs-setup label: nothing failed
//     and no judgment is owed, a human has to do something the run
//     cannot — put a secret in an environment, enable an API, create an
//     account. Without its own flavor it lands in Blocked looking like a
//     failure, and the Blocked column stops being readable as "what is
//     broken".
//   - scope-satisfied -> Blocked with the scope-satisfied label: the
//     ticket asks for what is already on main, so there was nothing to
//     build. Almost always a duplicate of merged work. Also where a run
//     that changed nothing and named no reason lands — a separate label
//     for that was tried and dropped, because it is the same status and
//     the same question, and the comment already says which happened.
//
// Every reason but "failed" parks in Blocked, including push-back. They
// want different things from a human — cancel the ticket, provision a
// secret, re-decide the design — and a Blocked column that renders them
// identically is one where every ticket has to be opened to be read.
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
		if strings.TrimSpace(message) == "" {
			return fmt.Errorf("abort: push-back without its argument is one the next pass repeats (DESIGN §3)")
		}
		// Blocked, not Designing, and this is a change from §2.7 as first
		// written. Routing straight back to Designing re-dispatches the
		// design agent — that is what entering Designing means — and
		// nothing counts the trips. A decisionless design pass and a dev
		// push-back can hand one ticket back and forth forever, each pass
		// correct on its own terms and neither able to see the loop it is
		// in. Counting the bounces was the alternative; parking is
		// cheaper and it puts the one participant who can actually break
		// the cycle in front of it.
		to = protocol.Blocked
		m = &marker.Marker{Kind: marker.Blocked, Fields: map[string]string{"pushback": "1"}}
		message = softLabel(ctx, p, res, message, core.LabelPushback)
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
		message = softLabel(ctx, p, res, message, core.LabelNeedsSetup)
	case "scope-satisfied":
		if strings.TrimSpace(message) == "" {
			return fmt.Errorf("abort: scope-satisfied without saying what is already there is a ticket nobody can adjudicate")
		}
		to = protocol.Blocked
		m = &marker.Marker{Kind: marker.Blocked, Fields: map[string]string{"scope-satisfied": "1"}}
		message = softLabel(ctx, p, res, message, core.LabelScopeSatisfied)
	default:
		return fmt.Errorf("abort: reason must be pushback, failed, needs-setup or scope-satisfied, got %q", reason)
	}
	if m != nil {
		m.Fields["from"] = string(res.State)
		message = m.Comment(message)
	}
	if res.Role == "" {
		// A claim with no role cannot record its move, and an unrecorded
		// move reads as the author's on the next sweep and is reverted.
		// Refusing here leaves the ticket where the run found it, which
		// the stale-claim rule then handles honestly.
		return fmt.Errorf("abort %s: the claim carries no role, so this move cannot be recorded", res.TicketKey)
	}
	if err := p.CommentTicket(ctx, res.TicketID, message); err != nil {
		return err
	}
	return p.TransitionTicket(ctx, res.TicketID, to, res.Role)
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
