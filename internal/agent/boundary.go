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
	"github.com/SwaggerAllen/orchestration/internal/retro"
)

// Boundary step names, in §10 order. Each completed step posts a
// boundary-step marker; on re-entry the agent resumes at the first step
// without one — that comment thread IS the resume mechanism (DESIGN §10).
const (
	StepArchive = "archive" // retro note + archive pass
	StepScan    = "scan"    // model: debt scan + grooming proposals
	StepFile    = "file"    // proposals filed, ranking applied
	// StepClose ends the pass. It is not a step to resume into — it is
	// the terminator that says the steps above belong to a pass that
	// finished, so a later entry to In progress starts a new one rather
	// than resuming a completed pass into nothing.
	StepClose = "close"
)

// BoundaryPlan is a boundary claim: the ticket plus which steps already
// completed.
type BoundaryPlan struct {
	ClaimResult
	Milestone string
	Done      map[string]bool
	// HarnessFindings are the pipeline problems agents recorded during
	// this milestone (DESIGN §10). Bounded like every other scan input:
	// markers on live tickets, deduped, not prose anybody has to read
	// their way through.
	HarnessFindings []HarnessFinding
	// Roster is every milestone in the tracker's order, with how much of
	// each is still open. The gating test asks about "the next product
	// milestone" (DESIGN §10), and this is where that comes from —
	// queried, not configured, so it cannot describe a convention the
	// project stopped following.
	Roster []MilestoneStatus
	// Backlog is the unscheduled tech-debt tickets with their current
	// priorities — the thing the grooming pass is asked to re-rank
	// (DESIGN §10). Carried because the run cannot read it: the model
	// holds the model credential and nothing else, so a prompt that does
	// not state the ordering is a prompt asking for a re-ranking of an
	// invisible list. Two boundaries in a row returned an empty ranking
	// for exactly that reason, which reads on the ticket as "the order
	// looked right".
	Backlog []CompositionEntry
}

// MilestoneStatus is one milestone as the boundary agent sees it.
type MilestoneStatus struct {
	Name string `json:"name"`
	// Open counts unresolved tickets; archived work is invisible here,
	// which is what the retro notes exist to compensate for (DESIGN §10).
	Open    int  `json:"open"`
	Current bool `json:"current"`
}

// ClaimBoundary asserts and marks. The author's Todo -> In progress move
// was the signal; the sweep dispatched us; nothing transitions here.
func ClaimBoundary(ctx context.Context, p *plane.Plane, ticketKey, dispatchID, dispatchURL string, now time.Time) (*BoundaryPlan, error) {
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
		return nil, fmt.Errorf("boundary claim: no ticket %q in the project scope", ticketKey)
	}
	if err := core.VerifyPickup(snap, t.ID, core.AgentBoundary); err != nil {
		return nil, err
	}

	plan := &BoundaryPlan{
		ClaimResult: ClaimResult{
			TicketID: t.ID, TicketKey: t.Key, Title: t.Title,
			Mode: "boundary", Role: core.RoleBoundary, Description: t.Description, State: t.State,
		},
		Milestone:       t.Milestone,
		Done:            map[string]bool{},
		HarnessFindings: CollectHarnessFindings(snap.Tickets),
	}
	// Only the steps of the pass in progress count. A boundary ticket
	// outlives its pass: the author closes the review, works the
	// proposals through the pipeline, and moves it back to In progress
	// for another look — which is a new pass, not a resume, and the
	// difference is invisible to a reader that just unions every step
	// marker on the ticket.
	//
	// It was invisible to this one. Catapult's ORC-45 ran a full pass on
	// 16 Aug, filed five tickets, and came back on 18 Aug for a second
	// look at the harness findings the runs since had recorded. The
	// claim read the old scan and file markers, the action skipped the
	// model step on `scan_done`, the file step skipped itself, and the
	// run reached Boundary review in three seconds having read nothing
	// and filed nothing — and overwrote the previous pass's composition
	// with an empty one on the way past.
	//
	// The close marker is what draws the line, and comments arrive
	// oldest first, so seeing one means everything above it is history.
	for _, c := range t.Comments {
		m, ok, err := marker.Parse(c.Body)
		if err == nil && ok && m.Kind == marker.BoundaryStep {
			if m.Fields["step"] == StepClose {
				plan.Comments = nil
				plan.Done = map[string]bool{}
				continue
			}
			plan.Done[m.Fields["step"]] = true
		}
		plan.Comments = append(plan.Comments, c.Body)
	}
	// A resumed run collects fewer findings than the first pass did, or
	// none: the tickets carrying them may already be archived by this
	// boundary's own archive step. Union rather than either alone —
	// live tickets are the source on a first pass, and the archive
	// step's carry is the source once it has run.
	plan.HarnessFindings = mergeFindings(plan.HarnessFindings, RecoverHarnessFindings(plan.Comments))

	milestones, err := p.Milestones(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range milestones {
		st := MilestoneStatus{Name: m.Name, Current: m.Name == t.Milestone}
		for _, cand := range snap.Tickets {
			if cand.Milestone == m.Name && !cand.IsBoundary() && !cand.Resolved() {
				st.Open++
			}
		}
		plan.Roster = append(plan.Roster, st)
	}

	plan.Backlog = DebtBacklog(snap)

	// The scan proposes tickets, so it is subject to the confirmed
	// non-asks the same way design is (DESIGN §4) — a debt scan that
	// files work the author already refused is worse than one that files
	// nothing.
	plan.NonAsks = ClaimNonAsks(p.Config)

	dm := marker.Marker{Kind: marker.Dispatch, Fields: map[string]string{
		"id":   dispatchID,
		"kind": "boundary",
		"url":  dispatchURL,
	}}
	if err := p.CommentTicket(ctx, t.ID, dm.Format()); err != nil {
		return nil, err
	}
	return plan, nil
}

func stepDone(ctx context.Context, p *plane.Plane, plan *BoundaryPlan, step, prose string) error {
	m := marker.Marker{Kind: marker.BoundaryStep, Fields: map[string]string{"step": step}}
	if err := p.CommentTicket(ctx, plan.TicketID, m.Comment(prose)); err != nil {
		return err
	}
	plan.Done[step] = true
	return nil
}

// BoundaryArchive is the archive pass (DESIGN §10 step 5): the retro note
// first — archiving silently breaks duplicate detection without it — then
// the milestone's Done issues are archived. Skips itself when its marker
// exists; every operation inside is idempotent anyway.
//
// The note is merged into rather than written once. A second pass over
// the same milestone archives the tickets that landed since the first,
// and a note that refused to be rewritten recorded none of them — their
// keys invisible to the next duplicate check, their merge shas gone from
// the rehearsal reset's revert list. Merging by issue key is idempotent
// for a resumed pass and additive for a new one, which is the property
// the write-once rule was reaching for.
func BoundaryArchive(ctx context.Context, p *plane.Plane, h host.Host, plan *BoundaryPlan, now time.Time) error {
	if plan.Done[StepArchive] {
		return nil
	}
	snap, err := p.Build(ctx, now, false)
	if err != nil {
		return err
	}
	var entries []retro.Entry
	var toArchive []string
	for _, t := range snap.Tickets {
		if t.Milestone != plan.Milestone || t.IsBoundary() {
			continue // the boundary ticket is the record of this pass
		}
		if t.State == protocol.Done {
			// The shas go in the note because this step is what takes
			// them away, exactly as the harness findings below do. The
			// rehearsal reset reads `merged` markers off the tickets it
			// is about to archive — and a boundary archives those same
			// tickets first, so a reset run after one found no tickets,
			// produced an empty merge list, and left the last
			// rehearsal's commits on main while reporting a green run.
			entries = append(entries, retro.Entry{Key: t.Key, Title: t.Title, SHAs: mergedSHAs(t)})
			toArchive = append(toArchive, t.ID)
		}
	}

	path := retro.Dir + "/" + slug(plan.Milestone) + ".md"
	prior, existed, err := h.ReadFile(ctx, path)
	if err != nil {
		return fmt.Errorf("boundary archive: reading the retro note: %w", err)
	}
	merged := retro.Merge(retro.Parse(prior), entries)
	if err := h.PutFile(ctx, path, retro.Render(plan.Milestone, merged), "retro: "+plan.Milestone); err != nil {
		return fmt.Errorf("boundary archive: retro note: %w", err)
	}
	for _, id := range toArchive {
		if err := p.Tracker.ArchiveIssue(ctx, id); err != nil {
			return fmt.Errorf("boundary archive: %w", err)
		}
	}
	prose := fmt.Sprintf("Archived %d Done issues. Retro note: %s", len(toArchive), path)
	if existed {
		prose += fmt.Sprintf(" (merged into an existing note; %d entries now)", len(merged))
	}
	// The findings ride out on this step's marker, because this step is
	// what destroys them. They live in comments on the tickets just
	// archived, and an archived issue vanishes from listings — so a run
	// that dies between here and the scan comes back to a claim that
	// collects nothing, and the milestone's harness findings are gone
	// with no trace that there were any. The same rule as the merge shas
	// in the note above, which this step was already breaking when this
	// comment was written: the information exists at exactly one moment,
	// and the step that ends that moment owns preserving it.
	if carried := carryFindings(plan.HarnessFindings); carried != "" {
		prose += "\n\n" + carried
	}
	return stepDone(ctx, p, plan, StepArchive, prose)
}

// mergedSHAs reads a ticket's merge commits off its `merged` markers,
// oldest first — the order the comments are in, which is the order the
// commits landed. All of them, not just the newest: a ticket merged,
// reverted by hand and merged again has two, and a note carrying one
// would leave half of it on main when the reset ran.
func mergedSHAs(t *core.Ticket) []string {
	var out []string
	for _, c := range t.Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok || m.Kind != marker.Merged {
			continue
		}
		if sha := m.Fields["sha"]; sha != "" {
			out = append(out, sha)
		}
	}
	return out
}

// Proposal is one debt-scan or grooming finding headed for Triage.
type Proposal struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	// Kind: "debt" or "design". Never "bug" — a bug parked in a queue has
	// been rescheduled rather than repaired (DESIGN §10).
	Kind string `json:"kind"`
	// Gating: does the next product milestone get materially harder
	// without it? (DESIGN §10's gating test.)
	Gating bool `json:"gating"`
	// Dedupe is milestone+finding; a re-run files nothing twice because
	// this key is checked against existing issues (DESIGN §10).
	Dedupe string `json:"dedupe"`
	// Subject is the concrete thing this proposal is about, named as the
	// repository names it: a file path, a config key, a mix task, a gate
	// line, a doc section, a module. The key is derived from it.
	//
	// Dedupe alone was the model's phrasing for one scan, and phrasing is
	// a choice rather than a fact — so two scans of one tree wrote two
	// keys for one finding and both filed. Measured on ORC-45: four
	// tickets for two findings, keyed `declared-gate-set-not-armed-in-ci`
	// and `arm-the-unarmed-gate-set` for the same gate work.
	//
	// A subject is not immune to rewording, but it is a fact about the
	// repository rather than a sentence about the finding, and two scans
	// naming one gate agree far more readily than two scans describing
	// it. Title similarity was measured as an alternative and rejected:
	// on the real ORC-45 pairs it scores 0.08 and 0.19 against a maximum
	// of 0.07 among unrelated proposals from the same scan, which is a
	// margin of one hundredth on a sample of two — a coincidence rather
	// than a threshold, and it vanishes entirely under stemming.
	Subject string `json:"subject"`
}

// dedupeKey is the key a proposal is filed under: derived from the
// subject when the scan named one, and falling back to the model's own
// key when it did not, so an older scan replayed by a resume still
// dedupes against what it filed.
func dedupeKey(milestone string, p Proposal) string {
	if strings.TrimSpace(p.Subject) == "" {
		return p.Dedupe
	}
	return slug(milestone) + "/" + slug(p.Subject)
}

// RankEntry re-ranks one existing ticket (grooming, DESIGN §10 step 7).
type RankEntry struct {
	Key      string `json:"key"`
	Priority int    `json:"priority"`
}

// Proposals is the boundary model's structured output.
type Proposals struct {
	Proposals []Proposal  `json:"proposals"`
	Ranking   []RankEntry `json:"ranking"`
}

// LoadProposals reads and validates the scan output.
func LoadProposals(path string) (*Proposals, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("proposals: %w", err)
	}
	return ParseProposals(raw)
}

// ParseProposals validates proposals from raw JSON.
func ParseProposals(raw []byte) (*Proposals, error) {
	var ps Proposals
	if err := json.Unmarshal(raw, &ps); err != nil {
		return nil, fmt.Errorf("proposals: %w", err)
	}
	for i, pr := range ps.Proposals {
		if pr.Title == "" || pr.Dedupe == "" {
			return nil, fmt.Errorf("proposal %d: title and dedupe are required — without the key a re-run files it twice (DESIGN §10)", i)
		}
		switch pr.Kind {
		case "debt", "design", "harness":
		default:
			return nil, fmt.Errorf("proposal %d (%s): kind %q — debt, design findings and harness findings only, never bugs (DESIGN §10)", i, pr.Title, pr.Kind)
		}
	}
	for i, r := range ps.Ranking {
		if r.Key == "" || r.Priority < 1 || r.Priority > 4 {
			return nil, fmt.Errorf("ranking %d: want a ticket key and priority 1-4, got %+v", i, r)
		}
	}
	return &ps, nil
}

// RecoverProposals reads the scan step's marker comment back into
// proposals — the resume path when the run died between scan and file.
func RecoverProposals(plan *BoundaryPlan) (*Proposals, error) {
	for i := len(plan.Comments) - 1; i >= 0; i-- {
		m, ok, err := marker.Parse(plan.Comments[i])
		if err != nil || !ok || m.Kind != marker.BoundaryStep || m.Fields["step"] != StepScan {
			continue
		}
		_, prose, found := strings.Cut(plan.Comments[i], "\n\n")
		if !found {
			return nil, fmt.Errorf("scan marker carries no proposals JSON")
		}
		return ParseProposals([]byte(prose))
	}
	return nil, fmt.Errorf("no scan step comment to recover proposals from")
}

// findingsCarry introduces the harness findings preserved on the archive
// step's comment. A sentinel rather than a fenced code block: the
// harness parses only its own writes here, and a fixed line is a
// cheaper contract than a markdown shape the retro path could collide
// with.
const findingsCarry = "Harness findings carried past the archive (they lived on the tickets this step removed):"

func carryFindings(fs []HarnessFinding) string {
	if len(fs) == 0 {
		return ""
	}
	raw, err := json.Marshal(fs)
	if err != nil {
		return ""
	}
	return findingsCarry + "\n" + string(raw)
}

// RecoverHarnessFindings reads back what the archive step preserved.
// Absent is not an error: a milestone with no findings writes none, and
// a boundary that has not archived yet has nothing to recover.
func RecoverHarnessFindings(comments []string) []HarnessFinding {
	for i := len(comments) - 1; i >= 0; i-- {
		m, ok, err := marker.Parse(comments[i])
		if err != nil || !ok || m.Kind != marker.BoundaryStep || m.Fields["step"] != StepArchive {
			continue
		}
		_, payload, found := strings.Cut(comments[i], findingsCarry)
		if !found {
			return nil
		}
		var fs []HarnessFinding
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &fs); err != nil {
			return nil
		}
		return fs
	}
	return nil
}

// mergeFindings unions two lists on the dedupe key, keeping order.
func mergeFindings(a, b []HarnessFinding) []HarnessFinding {
	seen := map[string]bool{}
	out := make([]HarnessFinding, 0, len(a)+len(b))
	for _, f := range append(append([]HarnessFinding{}, a...), b...) {
		if f.Dedupe == "" || seen[f.Dedupe] {
			continue
		}
		seen[f.Dedupe] = true
		out = append(out, f)
	}
	return out
}

// BoundaryFile records the scan, files proposals with dedupe, applies the
// ranking, and hands the ticket to the author (DESIGN §10 steps 6-9).
func BoundaryFile(ctx context.Context, p *plane.Plane, plan *BoundaryPlan, ps *Proposals, now time.Time) error {
	if !plan.Done[StepScan] {
		raw, err := json.Marshal(ps)
		if err != nil {
			return err
		}
		// The scan's output rides in the step comment so a crashed run
		// can resume filing without re-scanning (DESIGN §10).
		if err := stepDone(ctx, p, plan, StepScan, string(raw)); err != nil {
			return err
		}
	} else if ps == nil {
		var err error
		ps, err = RecoverProposals(plan)
		if err != nil {
			return err
		}
	}

	if !plan.Done[StepFile] {
		snap, err := p.Build(ctx, now, false)
		if err != nil {
			return err
		}
		keyToID := map[string]string{}
		for _, t := range snap.Tickets {
			keyToID[t.Key] = t.ID
		}
		// Read from the tracker, not from the snapshot. Build skips
		// triage-category states and FileTriageProposal files into
		// exactly those, so a dedupe set assembled from snap.Tickets
		// could never contain a filed proposal — the check was inert
		// rather than weak, and two identical keys would have made two
		// tickets as readily as two different ones did.
		filedAlready, err := p.ListTriageProposals(ctx)
		if err != nil {
			return fmt.Errorf("boundary file: reading open proposals: %w", err)
		}
		existing := map[string]bool{}
		for _, tp := range filedAlready {
			if tp.Dedupe != "" {
				existing[tp.Dedupe] = true
			}
		}

		filed, skipped := 0, 0
		var failures []string
		for _, prop := range ps.Proposals {
			key := dedupeKey(plan.Milestone, prop)
			if existing[key] {
				skipped++
				continue
			}
			// Held so the rest of this scan dedupes against it too: two
			// proposals from one scan can name one subject.
			existing[key] = true
			if err := p.FileTriageProposal(ctx, prop.Title, prop.Description, prop.Kind, prop.Subject, prop.Gating, key); err != nil {
				// Collected, not returned. Returning on the first error
				// left the tickets already filed in the tracker while the
				// step comment said the step never ran — the audit trail
				// and the tracker disagreeing, with no way to learn which
				// proposals landed except reading the new tickets and
				// matching dedupe markers by hand. It also made the blast
				// radius arbitrary: a bad proposal first files nothing, a
				// bad proposal last files everything, and nothing chooses
				// where it sits.
				//
				// All problems at once, which is the posture the audit
				// takes for the same reason.
				failures = append(failures, fmt.Sprintf("%q: %v", prop.Title, err))
				continue
			}
			filed++
		}
		ranked, unknown := 0, []string{}
		for _, r := range ps.Ranking {
			id, ok := keyToID[r.Key]
			if !ok {
				unknown = append(unknown, r.Key)
				continue
			}
			if err := p.Tracker.UpdateIssuePriority(ctx, id, r.Priority); err != nil {
				failures = append(failures, fmt.Sprintf("ranking %s: %v", r.Key, err))
				continue
			}
			ranked++
		}
		prose := fmt.Sprintf("Filed %d proposals (%d deduped), re-ranked %d tickets.", filed, skipped, ranked)
		if len(unknown) > 0 {
			prose += fmt.Sprintf(" Unknown keys skipped: %v.", unknown)
		}
		if len(failures) > 0 {
			prose += fmt.Sprintf("\n\n%d did not land, and this comment is the record of which:\n\n- %s",
				len(failures), strings.Join(failures, "\n- "))
		}
		// Written whatever happened, because the step *did* run and the
		// tracker already shows what it did. The failures ride in the
		// same comment rather than aborting the run: everything above
		// landed, and a resume would otherwise re-derive that from the
		// tickets themselves.
		if err := stepDone(ctx, p, plan, StepFile, prose); err != nil {
			return err
		}
		if len(failures) > 0 {
			return fmt.Errorf("boundary file: %d of %d proposals did not land (recorded on the ticket): %s",
				len(failures), len(ps.Proposals), strings.Join(failures, "; "))
		}
	}

	// The composition proposal is the last thing the author reads before
	// they take over, so it is posted after filing — it can only be
	// computed once this boundary's findings are tickets. Outside the
	// step guard on purpose: a resume that re-enters with filing already
	// done is a run whose composition may never have landed.
	if err := ProposeComposition(ctx, p, plan, now); err != nil {
		return fmt.Errorf("boundary file: composition: %w", err)
	}
	// Everything this pass owed is on the ticket, so close the pass
	// before handing it over. Posted last and not before, because it is
	// the thing that stops the next entry from resuming: a run that dies
	// anywhere above this line has to be resumable, and one that gets
	// past it has nothing left to resume into.
	if err := stepDone(ctx, p, plan, StepClose, "Boundary pass complete. Moving this back here starts a new pass — archive, debt scan and grooming run again over whatever has landed since."); err != nil {
		return err
	}
	return p.TransitionTicket(ctx, plan.TicketID, protocol.BoundaryReview, core.RoleBoundary)
}

func slug(s string) string {
	out := strings.ToLower(s)
	var b strings.Builder
	dash := false
	for _, r := range out {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}
