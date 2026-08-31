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
	if err := core.VerifyPickup(snap, t.ID, core.AgentBoundary, dispatchID); err != nil {
		return nil, err
	}
	// Past the window the dispatch reservation covers (DESIGN §6): this
	// run is claiming, so it is visible in the run list and the ordinary
	// singularity guard has it from here. Held any longer it only idles
	// the kind. After VerifyPickup, never before — a run that just lost
	// the race must not hand back the winner's lock, and the store's
	// release is holder-scoped so this one cannot.
	p.ReleaseDispatchReservation(ctx, core.AgentBoundary, t.ID)

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
			shas := core.MergedSHAs(t)
			if len(shas) == 0 {
				// The sweep backfills a missing `merged` marker while
				// the ticket sits in Merged (DESIGN §11), but it has to
				// catch it there: Catapult's ORC-181 was in Merged for
				// 28 seconds, and a sweep beat is minutes. This is the
				// same lookup at the one moment the SHAs are actually
				// needed, over the tickets being archived rather than
				// the whole board.
				//
				// The note is the last record of these commits — the
				// step archives the tickets straight after — so a miss
				// here is not recoverable later, and the reset that
				// reads it leaves the commits on main while reporting a
				// green run.
				merged, err := h.MergedPRsFor(ctx, t.Key)
				if err != nil {
					return fmt.Errorf("boundary archive: merged PRs for %s: %w", t.Key, err)
				}
				for _, m := range merged {
					shas = append(shas, m.MergeSHA)
				}
			}
			entries = append(entries, retro.Entry{Key: t.Key, Title: t.Title, SHAs: shas})
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

// Proposal is one debt-scan or grooming finding headed for Triage.
type Proposal struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	// Kind is "debt", "design", "harness" or "bug". It picks the label
	// the proposal is filed under, and the label decides which backlog
	// reads it.
	//
	// "bug" was refused here until this milestone, on the argument that
	// a bug parked in a queue has been rescheduled rather than repaired.
	// The argument is right about parking and wrong about which rule
	// prevents it: what keeps a defect from waiting is the split in
	// DESIGN §10 — work that must be fixed before the milestone closes
	// is filed against the *current* milestone. Closing the vocabulary
	// on top of that did not stop the boundary finding defects; it
	// stopped it naming them, and a defect it cannot name it files as
	// debt.
	//
	// Which is the parking, reached by obeying the rule against it. Debt
	// carries the `tech-debt` label, `tech-debt` is what DebtBacklog
	// draws, and the composition schedules that into the next debt
	// milestone — one milestone in two, against a floor of five.
	//
	// Measured on Catapult's ORC-90, from the twelve findings its
	// archive step carried: an outage that failed every sweep on the
	// project from 18:06 to 19:58, two boundary agents running
	// concurrently on one ticket at about twenty-two minutes of spend, a
	// preflight check holding the expected and live label sets and
	// comparing neither, and a prompt flag rendered false whatever the
	// harness had recorded. None of those is work on the shape of the
	// code rather than what it does, which is DESIGN §8's definition of
	// debt. Every one is something that does not do what it says.
	//
	// Safe to allow because a bug ticket needs no milestone to run:
	// nothing sequences a milestone-less ticket, so the queue takes it
	// as soon as the author accepts it out of Triage (DESIGN §8), and
	// Urgent is what makes it preempt. That is also why a bug never
	// enters the debt composition — it does not need a slot in it.
	Kind string `json:"kind"`
	// Gating: does the next product milestone get materially harder
	// without it? (DESIGN §10's gating test.)
	//
	// Read only by the composition, which draws tech-debt — so on a
	// `bug` proposal it records the judgment and schedules nothing. Not
	// rejected there: a bug the scan thinks is gating is worth the
	// author reading as such, and failing the parse over a field that
	// steers nothing would take the file step down for no gain.
	Gating bool `json:"gating"`
	// Dedupe names the carried harness finding this proposal answers, so
	// `unadjudicated` can tell a finding that was filed from one that was
	// dropped (DESIGN §10). That is now its only job.
	//
	// It used to be the filing key as well, and a re-run was supposed to
	// file nothing twice because of it. It never worked in either
	// direction — see the file step — and automatic deduplication was
	// removed rather than given a third key. Adjudication matching is a
	// different question: it asks whether *this pass* answered a finding
	// *this pass* was handed, which is one run and one vocabulary.
	Dedupe string `json:"dedupe"`
	// Subject is the concrete thing this proposal is about, named as the
	// repository names it: a file path, a config key, a mix task, a gate
	// line, a doc section, a module.
	//
	// Two jobs, neither of which is a key any more. It decides whether the
	// proposal is author-only (a workflow file no agent can push), and it
	// rides on the filed ticket so a human deduplicating by hand can sort
	// two similar proposals without reading both descriptions.
	//
	// It *was* the filing key, derived as `slug(milestone)/slug(subject)`,
	// on the reasoning that a subject is a fact about the repository
	// rather than a sentence about the finding. Both halves of that
	// failed on Catapult's ORC-118: two unrelated defects in
	// `.github/workflows/ci.yml` collapsed to one ticket, and two
	// proposals naming `lib/catapult/engine/commands/approve_gate.ex` and
	// `Catapult.Engine.Commands.ApproveGate` — one thing, two spellings —
	// did not collapse at all. A subject is a fact; which fact it names
	// is still a choice.
	Subject string `json:"subject"`
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
	// Declined are the carried findings this pass judged not worth a
	// ticket, with the reason.
	//
	// The prompt asked for this before there was anywhere to put it —
	// "say so plainly when one is not worth filing" — against a schema
	// of proposals and ranking, parsed by a bare json.Unmarshal over the
	// whole comment. Prose alongside the JSON fails that parse and takes
	// the file step down, so the pass had two options per finding: file
	// it, or drop it silently.
	//
	// Measured on Catapult's ORC-90: twelve findings carried past the
	// archive, one proposal filed and none of the twelve among them, no
	// record that any had been read. Declining most of them was probably
	// right — several were fixed that same week. The defect was that the
	// judgment left no trace.
	Declined []Decline `json:"declined"`
}

// Decline is a carried finding the pass read and chose not to file.
type Decline struct {
	// Dedupe is the carried finding's key, which is what ties the
	// decision to the thing decided.
	Dedupe string `json:"dedupe"`
	// Why is the argument. Required: "declined" with no reason is the
	// silent drop this field exists to replace, one field wider.
	Why string `json:"why"`
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
		if !protocol.Known(protocol.ProposalKinds, pr.Kind) {
			return nil, fmt.Errorf("proposal %d (%s): kind %q — one of %s (DESIGN §10)",
				i, pr.Title, pr.Kind, strings.Join(protocol.ProposalKinds, ", "))
		}
	}
	for i, r := range ps.Ranking {
		if r.Key == "" || r.Priority < 1 || r.Priority > 4 {
			return nil, fmt.Errorf("ranking %d: want a ticket key and priority 1-4, got %+v", i, r)
		}
	}
	for i, d := range ps.Declined {
		if strings.TrimSpace(d.Dedupe) == "" || strings.TrimSpace(d.Why) == "" {
			return nil, fmt.Errorf("declined %d: both the finding's dedupe key and the reason are required — a decline with neither is the silent drop this replaces (DESIGN §10)", i)
		}
	}
	return &ps, nil
}

// unadjudicated names the carried findings this pass neither filed nor
// declined, by dedupe key.
//
// Matched on the key rather than the title, because the key is the one
// field the finding's own contract calls stable — "name the thing, not
// the run" — while titles are rewritten between scans. Measured on
// ORC-45: two concurrent scans of one tree kept every harness key
// byte-identical while rewording the titles.
//
// A proposal counts as adjudicating a finding when it carries the key,
// which is why the boundary prompt asks for the carried key as the
// proposal's dedupe rather than a fresh one.
func unadjudicated(carried []HarnessFinding, ps *Proposals) []string {
	if len(carried) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, pr := range ps.Proposals {
		seen[findingName(pr.Dedupe)] = true
	}
	for _, d := range ps.Declined {
		seen[findingName(d.Dedupe)] = true
	}
	var out []string
	for _, f := range carried {
		if !seen[findingName(f.Dedupe)] {
			out = append(out, fmt.Sprintf("`%s` — %s", f.Dedupe, f.Title))
		}
	}
	return out
}

// findingName is the bare finding name from either key shape the two
// producers write. Both are legitimate and neither is going to change:
// a carried finding takes its key from the `id=` field of a recorded
// [pipeline:v1:harness-finding] marker, which is the bare name, while a
// scan proposal is keyed `<milestone-slug>/<name>`. Normalising at the
// comparison rather than at each producer is what keeps that true.
//
// Compared raw, the two namespaces sit in one set and never match
// across it. Measured on Catapult's ORC-118, second pass: the file step
// warned that `boundary-prompt-step-flags-always-false` "was neither
// filed nor declined" and would be lost, one second after the same pass
// filed it as ORC-137 under
// `the-authoring-loop/boundary-prompt-step-flags-always-false`.
//
// That warning is written to be acted on — it tells the author to carry
// the finding by hand or lose it — so a false one manufactures a
// duplicate of a ticket already in Triage. Since proposals are no
// longer deduplicated (the comment in BoundaryFile says why), nothing
// downstream catches that duplicate: this defect creates exactly the
// work that change handed to a human.
//
// Both directions are wrong, not just the one observed: a finding
// carried *with* a prefix would fail to match an unprefixed re-file the
// same way. Taking the last segment fixes both, and needs no producer
// to agree with any other.
func findingName(dedupe string) string {
	if i := strings.LastIndex(dedupe, "/"); i >= 0 {
		return strings.TrimSpace(dedupe[i+1:])
	}
	return strings.TrimSpace(dedupe)
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
		// Triage included, and that is the whole of the re-rank working.
		// `snap.Tickets` excludes triage-category states by
		// construction; unscheduled debt lives in exactly those until
		// the author gives it a milestone, which is why `DebtBacklog`
		// reads the same union rather than `snap.Tickets` alone.
		//
		// Resolved against `snap.Tickets` only, this map could not hold
		// a single key the grooming pass is for. Every ranking landed in
		// `unknown` and the step reported "re-ranked 0 tickets" as a
		// success. Measured on Catapult's ORC-118, second pass: both
		// ORC-135 and ORC-136 asked for priority 2, both had existed for
		// over an hour, both were skipped as unknown, and the
		// composition comment nine seconds later showed them still at 0.
		//
		// The pass read those keys — the prompt's debt backlog listed
		// them — so it declined a carried finding about the backlog
		// being unreadable, as fixed. The reading half worked; this line
		// is where the writing half went. A finding was closed on the
		// strength of output the same run contradicted.
		keyToID := map[string]string{}
		for _, t := range append(append([]*core.Ticket{}, snap.Tickets...), snap.Triage...) {
			keyToID[t.Key] = t.ID
		}
		// **Nothing is deduplicated here, deliberately.** Two attempts
		// at an automatic key both failed, in opposite directions, and
		// the second failed silently.
		//
		// The first keyed on the model's own `dedupe` string. Phrasing
		// is a choice rather than a fact, so two scans of one tree wrote
		// two keys for one finding and filed it twice.
		//
		// The second derived the key from the proposal's `subject` —
		// `slug(milestone)/slug(subject)` — on the reasoning that a
		// subject is a repository fact. It is, but it is not a key.
		// Measured on Catapult's ORC-118: seventeen proposals collapsed
		// to sixteen because `dashboard-ci-sobelow-config-https-ignore-stale`
		// and `boundary-compile-cache-hides-preexisting-violations` both
		// named `.github/workflows/ci.yml`. Two unrelated defects in one
		// file, and the second was dropped — while a decline note on the
		// same ticket told the author it had been filed. In the same run
		// `orc75-no-gate-compare-and-swap` (`lib/catapult/engine/commands
		// /approve_gate.ex`) and `orc75-no-role-assignment-model`
		// (`Catapult.Engine.Commands.ApproveGate`) named one thing two
		// ways and did *not* collide. So the rule both merged what it
		// should not and missed what it should have caught, and which it
		// did depended on whether the model wrote a path or a module
		// name.
		//
		// A duplicate ticket is visible at Boundary review and costs a
		// moment to decline. A dropped finding is invisible and lives
		// only on the archive step's comment until the next milestone
		// opens a new boundary ticket. Those costs are not symmetric,
		// and an automatic key that cannot be made deterministic should
		// not be the thing choosing between them.
		filed := 0
		var failures, gating []string
		for _, prop := range ps.Proposals {
			key, err := p.FileTriageProposal(ctx, prop.Title, prop.Description, prop.Kind, prop.Subject, prop.Gating)
			if err != nil {
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
			// Gating is collected as it is filed, because this comment
			// is the only place it is ever reported. The composition
			// proposal draws `tech-debt` and prints `gating=N` scoped to
			// that draw, so a gating **bug** — which never enters the
			// composition, by design, because it needs no milestone to
			// run — was counted nowhere and named nowhere. On ORC-118
			// two proposals were marked gating and the author's summary
			// read `gating=0`; the judgment survived only as a field in
			// a marker on each individual issue.
			if prop.Gating {
				gating = append(gating, fmt.Sprintf("%s — %s (%s)", key, prop.Title, prop.Kind))
			}
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
		prose := fmt.Sprintf("Filed %d proposals, re-ranked %d tickets. Nothing is deduplicated automatically — duplicates are yours to decline here (DESIGN §10).", filed, ranked)
		if len(unknown) > 0 {
			prose += fmt.Sprintf(" Unknown keys skipped: %v.", unknown)
		}
		if len(gating) > 0 {
			prose += fmt.Sprintf("\n\n**%d of them are gating** — the next product milestone gets materially harder without them (DESIGN §10). A gating `bug` does not appear in the composition proposal below, because a bug needs no milestone to run: accepting it out of Triage is what queues it.\n\n- %s",
				len(gating), strings.Join(gating, "\n- "))
		}
		if len(ps.Declined) > 0 {
			var lines []string
			for _, d := range ps.Declined {
				lines = append(lines, fmt.Sprintf("`%s` — %s", d.Dedupe, d.Why))
			}
			prose += fmt.Sprintf("\n\nDeclined %d carried finding(s), with the reason:\n\n- %s",
				len(ps.Declined), strings.Join(lines, "\n- "))
		}
		// Every carried finding has to be accounted for, and this is the
		// only moment anyone could notice that one was not.
		//
		// The findings the archive step carried lived on the tickets it
		// deleted. They survive on that step's own comment, which a
		// resume re-reads — so they persist across passes on this
		// ticket, and are gone the moment the next milestone opens a new
		// one. A finding neither filed nor declined is therefore not
		// merely unrecorded: it is on a clock nobody can see.
		//
		// Named rather than fatal. The pass did its work, the tickets it
		// filed are real, and failing here would throw that away over a
		// judgment the author is about to make anyway at Boundary
		// review. What the author cannot do is be a backstop for
		// something they are never shown.
		if missed := unadjudicated(plan.HarnessFindings, ps); len(missed) > 0 {
			prose += fmt.Sprintf("\n\n**%d carried finding(s) were neither filed nor declined.** They are recorded on this ticket's archive-step comment and nowhere else, so they are lost when the next milestone opens a new boundary ticket:\n\n- %s",
				len(missed), strings.Join(missed, "\n- "))
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
