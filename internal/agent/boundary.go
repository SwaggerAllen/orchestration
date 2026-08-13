package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Boundary step names, in §10 order. Each completed step posts a
// boundary-step marker; on re-entry the agent resumes at the first step
// without one — that comment thread IS the resume mechanism (DESIGN §10).
const (
	StepArchive = "archive" // retro note + archive pass
	StepScan    = "scan"    // model: debt scan + grooming proposals
	StepFile    = "file"    // proposals filed, ranking applied
)

// BoundaryPlan is a boundary claim: the ticket plus which steps already
// completed.
type BoundaryPlan struct {
	ClaimResult
	Milestone string
	Done      map[string]bool
	// Roster is every milestone in the tracker's order, with how much of
	// each is still open. The gating test asks about "the next product
	// milestone" (DESIGN §10), and this is where that comes from —
	// queried, not configured, so it cannot describe a convention the
	// project stopped following.
	Roster []MilestoneStatus
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
			Mode: "boundary", Description: t.Description, State: t.State,
		},
		Milestone: t.Milestone,
		Done:      map[string]bool{},
	}
	for _, c := range t.Comments {
		plan.Comments = append(plan.Comments, c.Body)
		m, ok, err := marker.Parse(c.Body)
		if err == nil && ok && m.Kind == marker.BoundaryStep {
			plan.Done[m.Fields["step"]] = true
		}
	}

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

	// The scan proposes tickets, so it is subject to the confirmed
	// non-asks the same way design is (DESIGN §4) — a debt scan that
	// files work the author already refused is worse than one that files
	// nothing.
	plan.NonAsks = claimNonAsks(p.Config)

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
func BoundaryArchive(ctx context.Context, p *plane.Plane, h host.Host, plan *BoundaryPlan, now time.Time) error {
	if plan.Done[StepArchive] {
		return nil
	}
	snap, err := p.Build(ctx, now, false)
	if err != nil {
		return err
	}
	var lines []string
	var toArchive []string
	for _, t := range snap.Tickets {
		if t.Milestone != plan.Milestone || t.IsBoundary() {
			continue // the boundary ticket is the record of this pass
		}
		if t.State == protocol.Done {
			lines = append(lines, fmt.Sprintf("- %s — %s", t.Key, t.Title))
			toArchive = append(toArchive, t.ID)
		}
	}
	sort.Strings(lines)

	path := "docs/retros/" + slug(plan.Milestone) + ".md"
	content := fmt.Sprintf("# Retro — %s\n\nShipped (archived from the tracker; this note is what duplicate detection reads):\n\n%s\n",
		plan.Milestone, strings.Join(lines, "\n"))
	created, err := h.PutFileIfAbsent(ctx, path, content, "retro: "+plan.Milestone)
	if err != nil {
		return fmt.Errorf("boundary archive: retro note: %w", err)
	}
	for _, id := range toArchive {
		if err := p.Tracker.ArchiveIssue(ctx, id); err != nil {
			return fmt.Errorf("boundary archive: %w", err)
		}
	}
	prose := fmt.Sprintf("Archived %d Done issues. Retro note: %s", len(toArchive), path)
	if !created {
		prose += " (already existed — resumed run)"
	}
	return stepDone(ctx, p, plan, StepArchive, prose)
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
		if pr.Kind != "debt" && pr.Kind != "design" {
			return nil, fmt.Errorf("proposal %d (%s): kind %q — design findings and debt only, never bugs (DESIGN §10)", i, pr.Title, pr.Kind)
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
		existing := map[string]bool{}
		keyToID := map[string]string{}
		for _, t := range snap.Tickets {
			keyToID[t.Key] = t.ID
			for _, line := range strings.Split(t.Description, "\n") {
				m, ok, err := marker.Parse(line)
				if err == nil && ok && m.Kind == marker.TriageProposal {
					existing[m.Fields["dedupe"]] = true
				}
			}
		}

		filed, skipped := 0, 0
		for _, prop := range ps.Proposals {
			if existing[prop.Dedupe] {
				skipped++
				continue
			}
			if err := p.FileTriageProposal(ctx, prop.Title, prop.Description, prop.Kind, prop.Gating, prop.Dedupe); err != nil {
				return fmt.Errorf("boundary file: %q: %w", prop.Title, err)
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
				return fmt.Errorf("boundary file: ranking %s: %w", r.Key, err)
			}
			ranked++
		}
		prose := fmt.Sprintf("Filed %d proposals (%d deduped), re-ranked %d tickets.", filed, skipped, ranked)
		if len(unknown) > 0 {
			prose += fmt.Sprintf(" Unknown keys skipped: %v.", unknown)
		}
		if err := stepDone(ctx, p, plan, StepFile, prose); err != nil {
			return err
		}
	}

	// The composition proposal is the last thing the author reads before
	// they take over, so it is posted after filing — it can only be
	// computed once this boundary's findings are tickets.
	if err := ProposeComposition(ctx, p, plan, now); err != nil {
		return fmt.Errorf("boundary file: composition: %w", err)
	}
	return p.TransitionTicket(ctx, plan.TicketID, protocol.BoundaryReview)
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
