package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/filemap"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// ClaimDesign is the design agent's pickup: a normal pass on a ticket in
// Ready for design, or a re-evaluate re-read on a dev queue ticket
// (DESIGN §7).
//
// A normal pass transitions Ready for design -> Designing, which is
// state-transition-as-claim, the same mechanism the dev agent uses
// (DESIGN §6). It did not used to: Designing was both the queue and the
// agent's own state, so there was nothing to move and no moment at which
// a ticket stopped being "waiting" and started being "worked on". A run
// that died before claiming left a ticket asserting an agent was on it —
// ORC-7 sat that way for 23 minutes.
//
// A re-read leaves the state alone, because the ticket is in the dev
// queue and design is only looking at it.
func ClaimDesign(ctx context.Context, p *plane.Plane, ticketKey, dispatchID, dispatchURL string, now time.Time) (*ClaimResult, error) {
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
		return nil, fmt.Errorf("design claim: no ticket %q in the project scope", ticketKey)
	}
	if err := core.VerifyPickup(snap, t.ID, core.AgentDesign, dispatchID); err != nil {
		return nil, err
	}
	// Past the window the dispatch reservation covers (DESIGN §6): this
	// run is claiming, so it is visible in the run list and the ordinary
	// singularity guard has it from here. Held any longer it only idles
	// the kind. After VerifyPickup, never before — a run that just lost
	// the race must not hand back the winner's lock, and the store's
	// release is holder-scoped so this one cannot.
	p.ReleaseDispatchReservation(ctx, core.AgentDesign, t.ID)

	mode := "design"
	if t.State != protocol.ReadyForDesign && t.State != protocol.ReadyForRedesign {
		mode = "design-reread"
	}
	res := &ClaimResult{
		TicketID: t.ID, TicketKey: t.Key, Title: t.Title,
		Mode:             mode,
		DesignOwnedPaths: append([]string(nil), p.Config.DesignOwnedPaths...),
		// Carried for the non-asks selection (DESIGN §4). Usually empty
		// here and that is expected — the design pass is what creates
		// the mutex labels (§6) — which is exactly why the selection
		// also reads the ticket's own words.
		Labels:      append([]string(nil), t.Labels...),
		Role:        core.RoleDesign,
		State:       t.State,
		Scope:       t.Description,
		Description: t.Description,
	}
	for _, c := range t.Comments {
		if prose, worth := marker.Prose(c.Body); worth {
			res.Comments = append(res.Comments, prose)
		}
	}
	// Read before proposing (DESIGN §4), and written back to in the same
	// artifacts commit when a push-back establishes a new refusal.
	res.NonAsks = ClaimNonAsks(p.Config)
	// What the project's own docs have already settled (ORC-126). Read
	// here beside the non-asks, and for the same reason: both are files
	// in the checkout the pass could open, and both are the inputs a
	// pass skips when nothing puts them in front of it.
	res.Decisions = ClaimDecisions(p.Config)
	if pr := p.PRForTicket(ctx, t.Key); pr != nil {
		res.Branch = pr.Branch
		res.PRNumber = pr.Number
	} else {
		res.Branch = deriveBranch(t.Key, t.Title)
	}

	// State-transition-as-claim, then the dispatch marker, in that order
	// and for the reason dev's claim gives: the move is what takes the
	// ticket out of the queue, and the marker is what makes the claim
	// auditable (DESIGN §6).
	if mode == "design" {
		if err := p.TransitionTicket(ctx, t.ID, protocol.Designing, core.RoleDesign); err != nil {
			return nil, err
		}
		res.State = protocol.Designing
	}
	m := marker.Marker{Kind: marker.Dispatch, Fields: map[string]string{
		"id":   dispatchID,
		"kind": "design",
		"url":  dispatchURL,
	}}
	if err := p.CommentTicket(ctx, t.ID, m.Format()); err != nil {
		return nil, err
	}
	return res, nil
}

// DesignOutcome is the design model's structured output.
type DesignOutcome struct {
	// Outcome by mode:
	//   design:        "artifacts" | "decisionless" | "prerequisite"
	//   design-reread: "clear" | "demote"
	Outcome string `json:"outcome"`
	// Screens and Systems are the touch lists; the harness turns them
	// into screen:<name> and system:<name> labels — the mutex is fed
	// here, and touching is not deciding: a decisionless pass still
	// declares systems (DESIGN §4, §6).
	Screens []string `json:"screens"`
	Systems []string `json:"systems"`
	// Summary is the argument for what was done or decided. Required for
	// everything but a plain artifacts pass, where it is still posted
	// when present.
	Summary string `json:"summary"`
}

// LoadDesignOutcome reads and validates the outcome for the given mode.
func LoadDesignOutcome(path, mode string) (*DesignOutcome, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("design outcome: %w (a design run that decided nothing did not design)", err)
	}
	var o DesignOutcome
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("design outcome %s: %w", path, err)
	}
	// `prerequisite` is a normal pass's third way out and is absent from
	// the re-read on purpose: a re-read runs on a ticket already in the
	// dev queue, where "the ground moved" is what `demote` is for.
	legal := map[string][]string{
		"design":        {"artifacts", "decisionless", "prerequisite"},
		"design-reread": {"clear", "demote"},
	}
	ok := false
	for _, l := range legal[mode] {
		if o.Outcome == l {
			ok = true
		}
	}
	if !ok {
		return nil, fmt.Errorf("design outcome: %q is not legal in mode %s (want one of %v)", o.Outcome, mode, legal[mode])
	}
	if o.Outcome == "decisionless" && len(o.Screens) > 0 {
		return nil, fmt.Errorf("design outcome: decisionless with screens %v is a contradiction — a screen touched is an artifact owed", o.Screens)
	}
	// Both lists, where decisionless refuses only screens. The difference
	// is that a decisionless pass examined the scope and knows what it
	// will touch, so its systems are real and the mutex should hold them.
	// A prerequisite pass could not examine the scope — that is what it
	// is reporting — so its touch list is a guess, and the mutex is the
	// wrong place to put a guess: `Blocked` is a started state (see
	// core.isStarted), so a label attached here holds the mutex against
	// every other ticket naming that system for as long as the ticket
	// sits parked, which is until a human moves it.
	if o.Outcome == "prerequisite" && (len(o.Screens) > 0 || len(o.Systems) > 0) {
		return nil, fmt.Errorf("design outcome: prerequisite declares screens %v and systems %v, but a pass that could not start has nothing to hold the mutex for — say what it is waiting on in the summary instead", o.Screens, o.Systems)
	}
	if o.Outcome != "artifacts" && strings.TrimSpace(o.Summary) == "" {
		return nil, fmt.Errorf("design outcome: %q without its argument is one the next pass repeats (DESIGN §3)", o.Outcome)
	}
	return &o, nil
}

// RecordReview is the record review's verdict on what this pass wrote
// to the record (DESIGN §4): a second, fresh model run inside the same
// design job, holding the pass's own doc diff, the rules and reasons
// behind the ids that diff touched as they stood before the pass, and
// the rule.
//
// Fresh on purpose. The design agent that writes pass narration is in
// the worst position to apply the rule against it: at the end of a long
// run, with the reasoning it just did as the freshest thing in its
// context — and the narration *is* that reasoning's residue — while the
// rule forbidding it sits a hundred thousand tokens behind. Measured on
// Catapult, 2026-09-01: 90 of 284 standing-decision bullets carried
// pass narration, every one written by a prompt that prohibits it in as
// many words and every one signed off at Design review. A reader with
// the diff, the reasons behind what it touched, and the rule has none of
// that pressure; reconcile is the same shape, for the same reason.
//
// The reasons are the one input added to that isolation, and they are
// selected by the diff, not by the ticket: a touched rule's entry is the
// record's own prior text about that rule, the removed side of the diff
// one file over. It carries nothing the pass wrote about its ticket. It
// is what lets the reviewer see a rule rewritten against its own reason
// — the contradiction kind — which from the diff alone is invisible.
//
// Detect here, fix there. The reviewer has the distance to see
// narration; the writer has the context to know which "Y" is a
// load-bearing incident and which is an alternative it passed over. A
// diff-only rewriter would delete the reason with the narration,
// silently, because from the diff alone they look the same. So a
// decline goes back to the design pass as feedback, through the
// author's own decline shape (DESIGN §3), and the rewrite is the
// writer's.
type RecordReview struct {
	// Verdict is "pass" or "decline" (protocol.RecordReviewVerdicts).
	Verdict string `json:"verdict"`
	// Findings are the passages sent back. Required on a decline —
	// a decline with nothing to point at is a rejection the next pass
	// repeats (DESIGN §3) — and permitted on a pass as callouts.
	Findings []RecordFinding `json:"findings"`
	// Summary is the reviewer's one-paragraph argument, optional.
	Summary string `json:"summary"`
}

// RecordFinding is one passage the review sent back.
type RecordFinding struct {
	// File is the repo-relative document the passage is in.
	File string `json:"file"`
	// Quote is enough of the passage to find it — the opening words,
	// not the whole bullet.
	Quote string `json:"quote"`
	// Why names what it reads as: a review round, a rejected
	// alternative, a prior draft. The writer decides what to do about
	// it; this only has to be findable.
	Why string `json:"why"`
	// Kind is one of protocol.RecordFindingKinds; absent reads as
	// narration, which is what every finding was before the kind
	// existed.
	Kind string `json:"kind,omitempty"`
	// ID names the rule a contradiction breaks, as a citation writes it
	// — `foundation#17` — and is required on that kind: the writer has
	// to find the entry, and the rule's wording is exactly what changed.
	ID string `json:"id,omitempty"`
}

// findingID is the citation grammar (reasons.ParseCite), held here as a
// regexp so the loader stays a loader: `caps#17` or `caps#ORC-247-2`.
var findingID = regexp.MustCompile(`^(?:(?:system|screen):)?[a-z0-9_-]+#(?:[A-Z][A-Z0-9]*-[0-9]+-)?[0-9]+$`)

// LoadRecordReview reads and validates the review the model wrote.
//
// A missing file is an error to the caller, not a pass: the action
// decides whether a review was owed at all, and a run that was owed one
// and produced nothing is reported as such rather than read as clean.
func LoadRecordReview(path string) (*RecordReview, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("record review: %w", err)
	}
	var r RecordReview
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("record review %s: %w", path, err)
	}
	if !protocol.Known(protocol.RecordReviewVerdicts, r.Verdict) {
		return nil, fmt.Errorf("record review: verdict %q is not one of %s", r.Verdict, strings.Join(protocol.RecordReviewVerdicts, ", "))
	}
	if r.Verdict == "decline" && len(r.Findings) == 0 {
		return nil, fmt.Errorf("record review: a decline with no findings is a rejection the next pass repeats (DESIGN §3)")
	}
	for i, f := range r.Findings {
		if strings.TrimSpace(f.File) == "" || strings.TrimSpace(f.Quote) == "" {
			return nil, fmt.Errorf("record review: finding %d names no file or no passage — the writer has to be able to find it", i+1)
		}
		if f.Kind == "" {
			r.Findings[i].Kind = "narration"
			continue
		}
		if !protocol.Known(protocol.RecordFindingKinds, f.Kind) {
			return nil, fmt.Errorf("record review: finding %d has kind %q, not one of %s", i+1, f.Kind, strings.Join(protocol.RecordFindingKinds, ", "))
		}
		if f.Kind == "contradiction" && !findingID.MatchString(strings.TrimSpace(f.ID)) {
			return nil, fmt.Errorf("record review: finding %d is a contradiction and names no rule (id %q) — a contradiction names the rule it contradicts, as name#n, because the writer has to find the entry", i+1, f.ID)
		}
	}
	return &r, nil
}

// prose renders the findings for the ticket. The marker line above it
// is for the sweep; this is for the next design pass, which reads the
// thread as scope (DESIGN §2.3).
func (r *RecordReview) prose() string {
	var b strings.Builder
	if r.Verdict == "decline" {
		b.WriteString("The record review read what this pass wrote to the record and sent it back (DESIGN §4). Each passage below either reads as narration — a review round, a draft that was thrown back, an alternative passed over — rather than as a rule and the reason it holds, or changes a rule without keeping or amending the reason recorded for it. Rewrite a narrated passage as the rule it establishes, keeping the reason where there is one; for a contradicted rule, run `pipeline reasons` on the id named and either keep the rule consistent with its reason or amend the entry in the same commit. The pass that wrote these has the context to settle each, and this review deliberately does not.\n")
	} else {
		b.WriteString("The record review read what this pass wrote to the record and found nothing to send back.")
		if len(r.Findings) > 0 {
			b.WriteString(" Callouts, not blockers:\n")
		}
	}
	for _, f := range r.Findings {
		if f.Kind == "contradiction" {
			fmt.Fprintf(&b, "\n- `%s` (contradicts `%s`): \u201c%s\u201d \u2014 %s", f.File, strings.TrimSpace(f.ID), strings.TrimSpace(f.Quote), strings.TrimSpace(f.Why))
			continue
		}
		fmt.Fprintf(&b, "\n- `%s`: \u201c%s\u201d \u2014 %s", f.File, strings.TrimSpace(f.Quote), strings.TrimSpace(f.Why))
	}
	if s := strings.TrimSpace(r.Summary); s != "" {
		b.WriteString("\n\n" + s)
	}
	return b.String()
}

// FinishDesign lands the outcome:
//
//   - artifacts     -> mutex labels ensured, draft PR opened, Design review
//   - decisionless  -> system labels still ensured (touching is not
//     deciding), decisionless-pass marker, straight to Ready for dev
//     (the §9 sign-off exception; the marker must exist before the
//     transition or the sweep reverts it)
//   - prerequisite  -> parked in Blocked under the prerequisite label,
//     through the same Abort the dev agent's named outcomes take
//   - clear      -> re-evaluate removed with the reasoning
//   - demote     -> back to Designing with the reasoning; the flag rides
//     along and the live pass folds it in (DESIGN §7)
//
// review is the record review's verdict on the pass's doc diff, nil when
// none was owed (nothing under designOwnedPaths changed). On an
// artifacts pass a decline takes the author's own decline route —
// findings on the ticket, back to Ready for design — and opens no PR
// and posts no preview: there is nothing to review yet. reviewErr is
// what a reviewer that died left behind; it is posted and the pass
// proceeds, because the review is a proposal about the record, never a
// gate on the work (DESIGN §4), and a broken reviewer must not cost a
// good design pass — but a review that silently did not run is a gate
// that looks armed and is not, so it is never silent.
func FinishDesign(ctx context.Context, p *plane.Plane, h host.Host, res *ClaimResult, o *DesignOutcome, previewURL, baseSHA string, changed, branchFiles []string, review *RecordReview, reviewErr string) error {
	// The ownership boundary, held before anything else lands. A pass
	// that wrote outside it does not get a draft PR and does not reach
	// Design review: returning here fails the finish step, and the
	// action's abort step parks the ticket in Blocked with the finding
	// already on it (DESIGN §5, §12).
	//
	// CI holds the same rule on the PR, and this is not a duplicate of
	// it. On a first pass there is no PR when the design finishes, so
	// CI has not run and cannot — this is the gate that stops the author
	// being asked to review a strayed diff as design. CI is the backstop
	// on every later push.
	//
	// After the push rather than before, deliberately. The strays are on
	// the branch by the time this runs and stay there — which is the
	// point: the author needs to read the diff to strip it, and a run
	// that swallowed its own output would leave a Blocked ticket with
	// nothing to look at. This stops the ticket advancing, not the
	// commit.
	//
	// An empty list passes rather than failing. The harness supplies it
	// from the branch diff, and a caller running an older action passes
	// none — losing the audit is the right cost there, where failing
	// would block every project whose stub predates it.
	for _, f := range filemap.DesignAudit(res.DesignOwnedPaths, changed) {
		if err := p.CommentTicket(ctx, res.TicketID, f); err != nil {
			return err
		}
		return fmt.Errorf("design finish %s: %s", res.TicketKey, f)
	}
	// Recorded before the outcome branches, on every pass that hands the
	// ticket forward: the base check is what warns the next dev pass that
	// main moved under the design (DESIGN §2.4), and a decisionless pass
	// hands the ticket to dev just as an artifacts pass does.
	if baseSHA != "" && (o.Outcome == "artifacts" || o.Outcome == "decisionless") {
		m := marker.Marker{Kind: marker.Base, Fields: map[string]string{"sha": baseSHA}}
		prose := fmt.Sprintf("Design was drawn against `%s`. The dev pass diffs against it; if main moved and both changes touch the same behavior, that is a push-back rather than a guess (DESIGN §2.4).", baseSHA)
		if err := p.CommentTicket(ctx, res.TicketID, m.Comment(prose)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(reviewErr) != "" {
		if err := p.CommentTicket(ctx, res.TicketID, "The record review did not run on this pass — its model run failed, and the pass proceeds without it rather than losing the design to a broken reviewer (DESIGN §4). What it printed:\n\n```\n"+strings.TrimSpace(reviewErr)+"\n```"); err != nil {
			return err
		}
	}
	// A decline is acted on only where there is a Design review to
	// withhold. Every other outcome hands the ticket somewhere a decline
	// has no meaning — decisionless goes to dev, prerequisite parks, the
	// re-read modes never wrote docs — so the findings are recorded and
	// nothing is swallowed, but the outcome stands.
	if review != nil && review.Verdict == "decline" && o.Outcome != "artifacts" {
		m := marker.Marker{Kind: marker.RecordReview, Fields: map[string]string{"verdict": "decline"}}
		note := fmt.Sprintf("Recorded, not acted on: this pass's outcome is `%s`, which does not go to Design review, so there is nothing for a decline to withhold.\n\n", o.Outcome)
		if err := p.CommentTicket(ctx, res.TicketID, m.Comment(note+review.prose())); err != nil {
			return err
		}
	}
	switch o.Outcome {
	case "artifacts":
		if review != nil && review.Verdict == "decline" {
			// The summary first, then the decline: the next design pass
			// reads the thread oldest-first and the newest comment is the
			// scope (DESIGN §2.3), so the argument this pass made stays
			// in the record and the findings are what it is handed.
			if o.Summary != "" {
				if err := p.CommentTicket(ctx, res.TicketID, o.Summary); err != nil {
					return err
				}
			}
			m := marker.Marker{Kind: marker.RecordReview, Fields: map[string]string{"verdict": "decline"}}
			if err := p.CommentTicket(ctx, res.TicketID, m.Comment(review.prose())); err != nil {
				return err
			}
			// No PR, no preview, no label reconciliation: nothing here is
			// ready to be reviewed, and the labels are the next pass's to
			// declare. The redesign queue, not Designing, for demote's
			// reason — and not Ready for design, because a bounce that
			// lands beside fresh tickets is a bounce nobody sees on the
			// board (DESIGN §3).
			return p.TransitionTicket(ctx, res.TicketID, protocol.ReadyForRedesign, core.RoleDesign)
		}
		if err := reconcileMutexLabels(ctx, p, res, o, branchFiles); err != nil {
			return err
		}
		if res.PRNumber == 0 {
			pr, err := h.CreatePR(ctx, res.Branch,
				fmt.Sprintf("%s %s", res.TicketKey, res.Title),
				fmt.Sprintf("Design artifacts for %s. One PR per ticket; dev pushes to this same branch (DESIGN §5).", res.TicketKey),
				true)
			if err != nil {
				return fmt.Errorf("design finish %s: draft PR: %w", res.TicketKey, err)
			}
			res.PRNumber = pr.Number
		}
		if o.Summary != "" {
			if err := p.CommentTicket(ctx, res.TicketID, o.Summary); err != nil {
				return err
			}
		}
		// Said rather than implied: a ticket with no review marker and
		// one whose review passed look the same, and only one of them
		// was checked. The author reading the thread at Design review
		// can tell which.
		if review != nil {
			m := marker.Marker{Kind: marker.RecordReview, Fields: map[string]string{"verdict": "pass"}}
			if err := p.CommentTicket(ctx, res.TicketID, m.Comment(review.prose())); err != nil {
				return err
			}
		}
		// Where to look, posted before the ticket asks to be looked at.
		// Design review is the author reading the rendered states
		// (DESIGN §4), and the ticket used to arrive in that state
		// without saying where they were — the URL had to be rebuilt
		// from a branch name and a Pages project name, by hand, every
		// time. Absent when the project has no preview wired, which is
		// silence rather than a broken link.
		if previewURL != "" {
			m := marker.Marker{Kind: marker.Preview, Fields: map[string]string{"url": previewURL}}
			prose := fmt.Sprintf("Storybook preview for this pass: %s\n\nThis is what Design review reads — the rendered states, alongside the doc diff on the PR.", previewURL)
			if err := p.CommentTicket(ctx, res.TicketID, m.Comment(prose)); err != nil {
				return err
			}
		}
		return p.TransitionTicket(ctx, res.TicketID, protocol.DesignReview, core.RoleDesign)

	case "decisionless":
		if err := reconcileMutexLabels(ctx, p, res, o, branchFiles); err != nil {
			return err
		}
		m := marker.Marker{Kind: marker.DecisionlessPass, Fields: map[string]string{}}
		if err := p.CommentTicket(ctx, res.TicketID, m.Comment(o.Summary)); err != nil {
			return err
		}
		return p.TransitionTicket(ctx, res.TicketID, protocol.ReadyForDev, core.RoleDesign)

	case "prerequisite":
		// Through Abort rather than transitioning here, for the reason
		// dev's named outcomes go through it: the park is not just a
		// state write. It stamps the state the run was working in, soft-
		// attaches the label, and records the move — and a second copy of
		// that in this switch is a second copy to keep in step.
		return Abort(ctx, p, res, "prerequisite", o.Summary, "")

	case "clear":
		if err := p.CommentTicket(ctx, res.TicketID, o.Summary); err != nil {
			return err
		}
		return p.RemoveTicketLabel(ctx, res.TicketID, core.LabelReEvaluate)

	case "demote":
		if err := p.CommentTicket(ctx, res.TicketID, o.Summary); err != nil {
			return err
		}
		// The queue, not Designing: dispatch reads the queues, so
		// demoting into Designing would park the ticket in a state
		// nothing picks up — a ticket sent back for redesign that no
		// design pass ever runs on. The redesign queue by name: the
		// summary above is the scope, and the state says why it is
		// there (DESIGN §3).
		return p.TransitionTicket(ctx, res.TicketID, protocol.ReadyForRedesign, core.RoleDesign)
	}
	return fmt.Errorf("design finish %s: unknown outcome %q", res.TicketKey, o.Outcome)
}

// reconcileMutexLabels makes the ticket's mutex labels the pass's
// declared touch list: the declared ones are attached, creating per-name
// labels on demand (DESIGN §6), and ones the ticket carries that this
// pass did not declare are released.
//
// The release half was missing entirely, and the labels were add-only
// from the day they were written. A design pass narrowing its scope is
// routine — the author sends a ticket back from Design review and the
// next pass draws less — and the label the first pass took stayed on the
// ticket holding the mutex against every other ticket naming that
// system, with no legal way for any pass to clear it. Catapult's ORC-141
// sat on `system:delivery` that way; only a direct tracker write got it
// off.
//
// **A label the branch's own files require is never released**, whatever
// the touch list says. CI derives the labels it demands from the diff
// (filemap.Audit), so releasing one the diff needs would fail the next
// push — and the pass that narrowed cannot know what an earlier pass or
// a dev round already put on the branch. Design's touch list is a
// prediction; the branch is evidence, and evidence wins. The kept label
// is reported rather than dropped silently: it means the pass and the
// branch disagree about the ticket's scope, which is a thing for a
// person to look at.
//
// branchFiles is everything the branch changed against main, and an
// empty one means nobody looked rather than a branch that touches
// nothing — the same reading resolveDiscoveredLabel gives its own diff,
// and for the same reason. Nothing is released without it: a wiring gap
// must not read as permission.
//
// Only screen: and system: labels are considered. Every other label on
// the ticket belongs to somebody else — the author, the sweep, the
// boundary — and a design pass has no business with them.
func reconcileMutexLabels(ctx context.Context, p *plane.Plane, res *ClaimResult, o *DesignOutcome, branchFiles []string) error {
	if err := verifyDeclaredDocs(p.Config.Root, o); err != nil {
		return err
	}
	declared := map[string]bool{}
	for _, s := range o.Screens {
		declared[protocol.ScreenLabelPrefix+s] = true
		if err := p.EnsureMutexLabel(ctx, res.TicketID, protocol.ScreenLabelPrefix+s); err != nil {
			return err
		}
	}
	for _, s := range o.Systems {
		declared[protocol.SystemLabelPrefix+s] = true
		if err := p.EnsureMutexLabel(ctx, res.TicketID, protocol.SystemLabelPrefix+s); err != nil {
			return err
		}
	}
	stale := staleMutexLabels(res.Labels, declared)
	if len(stale) == 0 {
		return nil
	}
	if len(branchFiles) == 0 {
		return p.CommentTicket(ctx, res.TicketID, fmt.Sprintf(
			"This pass did not declare %s, which the ticket carries. No branch-file list reached the finish step, so nothing could confirm the branch is done with them and they were left in place — they hold the mutex until somebody removes them by hand (DESIGN §6).",
			strings.Join(backticked(stale), ", ")))
	}
	required, err := requiredMutexLabels(p.Config.Root, branchFiles)
	if err != nil {
		return err
	}
	var released, kept []string
	for _, l := range stale {
		if required[l] {
			kept = append(kept, l)
			continue
		}
		if err := p.RemoveTicketLabel(ctx, res.TicketID, l); err != nil {
			// Soft, like the abort labels: a release that fails leaves
			// the mutex where it was, which is where it already is. The
			// pass's outcome is not worth failing over it.
			kept = append(kept, l)
			continue
		}
		released = append(released, l)
	}
	var parts []string
	if len(released) > 0 {
		parts = append(parts, fmt.Sprintf("Released %s: this pass did not declare them and nothing on the branch is mapped by their docs, so they were holding the mutex for work that is not happening (DESIGN §6).", strings.Join(backticked(released), ", ")))
	}
	if len(kept) > 0 {
		parts = append(parts, fmt.Sprintf("Kept %s although this pass did not declare them: the branch already carries files their docs map, and CI derives the labels it requires from the diff. The pass and the branch disagree about this ticket's scope — worth a look.", strings.Join(backticked(kept), ", ")))
	}
	if len(parts) == 0 {
		return nil
	}
	return p.CommentTicket(ctx, res.TicketID, strings.Join(parts, "\n\n"))
}

// staleMutexLabels is the mutex labels held but not declared, in the
// order the ticket carries them.
func staleMutexLabels(held []string, declared map[string]bool) []string {
	var out []string
	for _, l := range held {
		if !strings.HasPrefix(l, protocol.ScreenLabelPrefix) && !strings.HasPrefix(l, protocol.SystemLabelPrefix) {
			continue
		}
		if declared[l] {
			continue
		}
		out = append(out, l)
	}
	return out
}

// requiredMutexLabels is what CI would demand of this branch's diff.
func requiredMutexLabels(root string, branchFiles []string) (map[string]bool, error) {
	systems, err := filemap.LoadDir(filepath.Join(root, "systems"))
	if err != nil {
		return nil, fmt.Errorf("design finish: reading systems/: %w", err)
	}
	screens, err := filemap.LoadDir(filepath.Join(root, "screens"))
	if err != nil {
		return nil, fmt.Errorf("design finish: reading screens/: %w", err)
	}
	out := map[string]bool{}
	for _, l := range filemap.OwnerLabels(systems, screens, branchFiles) {
		out[l] = true
	}
	return out, nil
}

func backticked(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, "`"+s+"`")
	}
	return out
}

// verifyDeclaredDocs refuses a touch list naming a doc that does not
// exist, before the label is created from it.
//
// The label and the doc filename are one string in two places: the CI
// audit derives the label it requires as "system:" plus the doc's
// filename without .md (internal/filemap), and nothing until now
// compared the declared name against the directory. A design pass that
// wrote core-dsl where the doc is core_dsl.md therefore produced
// system:core-dsl — a real label, taking part in the mutex, that no
// diff could ever satisfy.
//
// Catapult's ORC-5 is the measurement. The mismatch survived the design
// pass, the author's sign-off and a full dev run — 22 modules, 60 tests,
// three commits — and surfaced as 28 audit violations on every path the
// ticket was about, at which point the only available outcome was a
// push-back asking a human to rename a label. The information needed to
// refuse was present at the moment the label was created.
//
// A directory with no docs at all is not an error and is not checked:
// the audit iterates docs, so with none of them no path is mapped, no
// label is required, and a declared name constrains nothing. Refusing
// there would fail every project that has not written its first system
// doc, over a label that cannot fail CI.
func verifyDeclaredDocs(root string, o *DesignOutcome) error {
	var problems []string
	check := func(dir, prefix string, declared []string) error {
		if len(declared) == 0 {
			return nil
		}
		docs, err := filemap.LoadDir(filepath.Join(root, dir))
		if err != nil {
			return fmt.Errorf("design finish: reading %s/: %w", dir, err)
		}
		if len(docs) == 0 {
			return nil
		}
		known := map[string]bool{}
		for _, d := range docs {
			known[d.Name] = true
		}
		for _, name := range declared {
			if known[name] {
				continue
			}
			msg := fmt.Sprintf("%s%s names %s/%s.md, which does not exist", prefix, name, dir, name)
			if near := nearestDoc(name, docs); near != "" {
				// The near miss is almost always the whole story — a
				// hyphen for an underscore — and naming it turns a
				// refusal into an instruction.
				msg += fmt.Sprintf(" (did you mean %s%s, for %s/%s.md?)", prefix, near, dir, near)
			}
			problems = append(problems, msg)
		}
		return nil
	}
	if err := check("screens", protocol.ScreenLabelPrefix, o.Screens); err != nil {
		return err
	}
	if err := check("systems", protocol.SystemLabelPrefix, o.Systems); err != nil {
		return err
	}
	if len(problems) == 0 {
		return nil
	}
	// All of them, not the first: a pass that declared two bad names
	// should not have to be run twice to learn the second one.
	return fmt.Errorf("design finish: the touch list names %d doc(s) that do not exist, and a mutex label the CI audit derives from a filename can only be satisfied by that exact spelling (DESIGN §6, §9):\n  - %s",
		len(problems), strings.Join(problems, "\n  - "))
}

// nearestDoc finds a doc whose name differs from the declared one only
// in the separators and case — the mismatch that actually happens,
// since both spellings read identically to a person.
func nearestDoc(name string, docs []filemap.Doc) string {
	fold := func(s string) string {
		return strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(s))
	}
	want := fold(name)
	for _, d := range docs {
		if fold(d.Name) == want {
			return d.Name
		}
	}
	return ""
}
