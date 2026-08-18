package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/plane"
)

// NonGatingFloor is DESIGN §10's minimum: a debt milestone takes all
// gating debt plus at least this many non-gating tickets by priority.
// Without the floor a heavy gating set means non-gating debt never runs
// and accumulates permanently.
const NonGatingFloor = 5

// CompositionEntry is one candidate for the next debt milestone.
type CompositionEntry struct {
	Key      string
	Title    string
	Gating   bool
	Priority int
}

// DebtBacklog is the unscheduled tech-debt tickets, in the order the
// composition rule reads them: gating first, then by priority (1 =
// Urgent is highest; Linear's 0 means "no priority", which sorts last
// rather than first), then key so the list is stable across re-runs.
//
// Shared by the two passes that need it, which want it at different
// moments. The composition proposal computes it after filing, when this
// boundary's own findings are tickets. The grooming pass needs it at
// claim, before the model runs — DESIGN §10 asks that pass to re-rank
// the debt backlog, and it had no way to see one: the prompt carried the
// milestone roster and nothing else, so two boundaries in a row returned
// an empty ranking because they could not read the ordering, not because
// the ordering looked right.
//
// Reads the snapshot's Triage list as well as its tickets, and the
// composition is why. Filed proposals sit in a triage-category state,
// which Build skips — so a composition running seconds after the file
// step could not see a single thing that step had just created. On
// Catapult's ORC-45 that printed "Nothing to schedule — no unscheduled
// tech-debt tickets" directly beneath "Filed 8 proposals". The code
// already said this was meant to work: the composition is posted after
// filing precisely because it "can only be computed once this
// boundary's findings are tickets".
//
// An unaccepted proposal is a candidate, not a commitment. Naming one
// here proposes it for the next debt milestone, which is the same thing
// the author is about to accept or decline — and assigning the
// milestone stays theirs either way.
func DebtBacklog(snap *core.Snapshot) []CompositionEntry {
	var out []CompositionEntry
	for _, t := range append(append([]*core.Ticket{}, snap.Tickets...), snap.Triage...) {
		if t.Resolved() || t.IsBoundary() || t.Milestone != "" {
			continue // resolved, machinery, or already scheduled
		}
		if !t.HasLabel("tech-debt") {
			continue
		}
		out = append(out, CompositionEntry{
			Key: t.Key, Title: t.Title, Priority: t.Priority,
			Gating: gatingFromDescription(t),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Gating != b.Gating {
			return a.Gating
		}
		if pri(a.Priority) != pri(b.Priority) {
			return pri(a.Priority) < pri(b.Priority)
		}
		return a.Key < b.Key
	})
	return out
}

// ProposeComposition computes what the next debt milestone should hold
// and posts it on the boundary ticket (DESIGN §10). It proposes only —
// assigning a milestone is a commitment, and the invariant that
// unstarted current-milestone work sits in Todo would turn any
// auto-assignment into auto-committed scope. The author enacts it.
//
// Candidates are unresolved tech-debt tickets not already scheduled into
// a milestone. Gating is read from each ticket's triage-proposal marker,
// which is where the boundary recorded that judgment when it filed them.
func ProposeComposition(ctx context.Context, p *plane.Plane, plan *BoundaryPlan, now time.Time) error {
	snap, err := p.Build(ctx, now, false)
	if err != nil {
		return err
	}

	candidates := DebtBacklog(snap)

	var chosen []CompositionEntry
	nonGating := 0
	for _, c := range candidates {
		if c.Gating {
			chosen = append(chosen, c)
			continue
		}
		if nonGating < NonGatingFloor {
			chosen = append(chosen, c)
			nonGating++
		}
	}

	gatingCount := len(chosen) - nonGating
	var b strings.Builder
	fmt.Fprintf(&b, "Proposed contents for the next debt milestone: %d gating + %d non-gating.\n\n",
		gatingCount, nonGating)
	if len(chosen) == 0 {
		b.WriteString("Nothing to schedule — no unscheduled tech-debt tickets. An honest empty beats a padded one (DESIGN §2.9).\n")
	}
	for _, c := range chosen {
		kind := "non-gating"
		if c.Gating {
			kind = "GATING"
		}
		fmt.Fprintf(&b, "- %s — %s  (%s, priority %d)\n", c.Key, c.Title, kind, c.Priority)
	}
	if nonGating < NonGatingFloor && len(candidates) > len(chosen) {
		fmt.Fprintf(&b, "\nNote: only %d non-gating tickets available against a floor of %d.\n", nonGating, NonGatingFloor)
	} else if nonGating < NonGatingFloor {
		fmt.Fprintf(&b, "\nNote: the backlog holds only %d non-gating debt tickets, below the floor of %d — the floor is a minimum to draw, not a quota to invent (DESIGN §10).\n", nonGating, NonGatingFloor)
	}
	b.WriteString("\nThis is a proposal. Assigning a milestone commits the work, which is yours to do — accept by setting the milestone on these tickets.\n")

	keys := make([]string, 0, len(chosen))
	for _, c := range chosen {
		keys = append(keys, c.Key)
	}
	m := marker.Marker{Kind: marker.Composition, Fields: map[string]string{
		"gating":     fmt.Sprintf("%d", gatingCount),
		"non_gating": fmt.Sprintf("%d", nonGating),
		"keys":       strings.Join(keys, " "),
	}}
	return p.CommentTicket(ctx, plan.TicketID, m.Comment(b.String()))
}

// pri normalises Linear's priority for sorting: 1 (Urgent) is highest and
// 0 ("no priority") is lowest, not first.
func pri(p int) int {
	if p == 0 {
		return 99
	}
	return p
}

// gatingFromDescription reads the gating judgment the boundary recorded
// when it filed the proposal.
func gatingFromDescription(t *core.Ticket) bool {
	for _, line := range strings.Split(t.Description, "\n") {
		m, ok, err := marker.Parse(line)
		if err == nil && ok && m.Kind == marker.TriageProposal {
			return m.Fields["gating"] == "true"
		}
	}
	return false
}
