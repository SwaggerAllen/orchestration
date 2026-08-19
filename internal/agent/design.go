package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

	mode := "design"
	if t.State != protocol.ReadyForDesign {
		mode = "design-reread"
	}
	res := &ClaimResult{
		TicketID: t.ID, TicketKey: t.Key, Title: t.Title,
		Mode:        mode,
		Role:        core.RoleDesign,
		State:       t.State,
		Scope:       t.Description,
		Description: t.Description,
	}
	for _, c := range t.Comments {
		res.Comments = append(res.Comments, c.Body)
	}
	// Read before proposing (DESIGN §4), and written back to in the same
	// artifacts commit when a push-back establishes a new refusal.
	res.NonAsks = ClaimNonAsks(p.Config)
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
	//   design:        "artifacts" | "decisionless"
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
	legal := map[string][]string{
		"design":        {"artifacts", "decisionless"},
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
	if o.Outcome != "artifacts" && strings.TrimSpace(o.Summary) == "" {
		return nil, fmt.Errorf("design outcome: %q without its argument is one the next pass repeats (DESIGN §3)", o.Outcome)
	}
	return &o, nil
}

// FinishDesign lands the outcome:
//
//   - artifacts     -> mutex labels ensured, draft PR opened, Design review
//   - decisionless  -> system labels still ensured (touching is not
//     deciding), decisionless-pass marker, straight to Ready for dev
//     (the §9 sign-off exception; the marker must exist before the
//     transition or the sweep reverts it)
//   - clear      -> re-evaluate removed with the reasoning
//   - demote     -> back to Designing with the reasoning; the flag rides
//     along and the live pass folds it in (DESIGN §7)
func FinishDesign(ctx context.Context, p *plane.Plane, h host.Host, res *ClaimResult, o *DesignOutcome, previewURL, baseSHA string) error {
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
	switch o.Outcome {
	case "artifacts":
		if err := ensureMutexLabels(ctx, p, res.TicketID, o); err != nil {
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
		if err := ensureMutexLabels(ctx, p, res.TicketID, o); err != nil {
			return err
		}
		m := marker.Marker{Kind: marker.DecisionlessPass, Fields: map[string]string{}}
		if err := p.CommentTicket(ctx, res.TicketID, m.Comment(o.Summary)); err != nil {
			return err
		}
		return p.TransitionTicket(ctx, res.TicketID, protocol.ReadyForDev, core.RoleDesign)

	case "clear":
		if err := p.CommentTicket(ctx, res.TicketID, o.Summary); err != nil {
			return err
		}
		return p.RemoveTicketLabel(ctx, res.TicketID, core.LabelReEvaluate)

	case "demote":
		if err := p.CommentTicket(ctx, res.TicketID, o.Summary); err != nil {
			return err
		}
		// The queue, not Designing: dispatch reads Ready for design, so
		// demoting into Designing would park the ticket in a state
		// nothing picks up — a ticket sent back for redesign that no
		// design pass ever runs on.
		return p.TransitionTicket(ctx, res.TicketID, protocol.ReadyForDesign, core.RoleDesign)
	}
	return fmt.Errorf("design finish %s: unknown outcome %q", res.TicketKey, o.Outcome)
}

// ensureMutexLabels attaches the declared touch lists as mutex labels,
// creating per-name labels on demand (DESIGN §6).
func ensureMutexLabels(ctx context.Context, p *plane.Plane, ticketID string, o *DesignOutcome) error {
	if err := verifyDeclaredDocs(p.Config.Root, o); err != nil {
		return err
	}
	for _, s := range o.Screens {
		if err := p.EnsureMutexLabel(ctx, ticketID, protocol.ScreenLabelPrefix+s); err != nil {
			return err
		}
	}
	for _, s := range o.Systems {
		if err := p.EnsureMutexLabel(ctx, ticketID, protocol.SystemLabelPrefix+s); err != nil {
			return err
		}
	}
	return nil
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
