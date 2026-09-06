// Package stats derives the pipeline's measurement of itself from the
// tracker's own history (DESIGN §13).
//
// Everything here is a pure function over what the tracker already
// stores. That is the whole architecture: Linear holds each ticket's
// complete state history, so a backfill and a nightly pass are the same
// code with a different window, and a defect in the derivation is
// repaired by running it again rather than by migrating what it wrote.
//
// No I/O. The adapter fetches, this shapes, the store keeps.
package stats

import (
	"sort"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
)

// DebtPrefix marks a tech-debt milestone by its name.
//
// A title match rather than a config field, deliberately. A config field
// would be a two-repo ordered change (`config.Load` calls
// `DisallowUnknownFields`, so the key has to land here first) for one
// boolean about six milestones. The separator is U+00B7, matching what
// the tracker actually holds — "Tech debt · before the engine" — and all
// three debt milestones use it.
const DebtPrefix = "Tech debt · "

// Milestone kinds. Two, because the question is "does debt work look
// different from feature work" and a third value would need a caller.
const (
	KindFeature = "feature"
	KindDebt    = "debt"
)

// Transition is one state change from the tracker's history: the state
// entered and when. `From` is carried only for the first entry, which is
// the only place it says something history does not — the state the
// ticket was created in.
type Transition struct {
	From string
	To   string
	At   time.Time
}

// Issue is one ticket as the collector reads it. Deliberately not
// tracker.Issue: that type carries comments, relations and resolved
// roles the sweep needs and this does not, and it comes from a query
// that excludes archived issues.
type Issue struct {
	Key       string
	Title     string
	Labels    []string
	Priority  int
	Milestone string
	CreatedAt time.Time
	// Zero when the ticket has not reached that end.
	CompletedAt time.Time
	CanceledAt  time.Time
	ArchivedAt  time.Time
	// CurrentState is what the ticket is in now, and it is the only
	// source for a ticket that has never moved: history is empty, so
	// nothing else says what state its one interval is.
	CurrentState string
	// History is oldest first. Every adapter must guarantee it — the
	// same requirement tracker.Issue.Comments carries, for the same
	// reason: Linear's connections come back newest first, and a
	// reversed history here silently inverts every interval.
	History []Transition
}

// IsBoundary reports whether an issue is a milestone boundary ticket.
func (i Issue) IsBoundary() bool {
	for _, l := range i.Labels {
		if l == core.LabelBoundary {
			return true
		}
	}
	return false
}

// Archived reports whether the tracker has archived this issue.
//
// Archived issues are counted like any other. Linear's archive is a
// list-visibility flag rather than a deletion — an archived ticket still
// returns its full state history — and the archived ones are the oldest,
// so dropping them would under-count the earliest milestones and render
// as a downward trend with nothing about the output looking wrong.
func (i Issue) Archived() bool { return !i.ArchivedAt.IsZero() }

// Interval is one span the ticket spent in one state. LeftAt is zero
// while the ticket is still in it.
type Interval struct {
	State     string
	EnteredAt time.Time
	LeftAt    time.Time
}

// Duration is how long the interval lasted, zero while it is still open.
//
// An open interval counts as zero rather than as "now minus entered".
// The alternative makes every read of a still-open ticket a different
// number, so the same query answered twice disagrees with itself — and
// the trend questions are all about tickets that finished.
func (iv Interval) Duration() time.Duration {
	if iv.LeftAt.IsZero() {
		return 0
	}
	return iv.LeftAt.Sub(iv.EnteredAt)
}

// Intervals turns an issue's transition history into the spans it spent
// in each state.
//
// The first interval is the one history cannot state on its own: it
// starts at creation, in the state the first transition moved *away*
// from. Every later interval starts where the previous one ended, and
// the last is left open.
func Intervals(i Issue) []Interval {
	if len(i.History) == 0 {
		// Never moved. One open interval in whatever it is in now —
		// and CurrentState is the only thing that can say, which is
		// why it is on the type.
		if i.CurrentState == "" {
			return nil
		}
		return []Interval{{State: i.CurrentState, EnteredAt: i.CreatedAt}}
	}
	out := make([]Interval, 0, len(i.History)+1)
	if from := i.History[0].From; from != "" {
		out = append(out, Interval{
			State:     from,
			EnteredAt: i.CreatedAt,
			LeftAt:    i.History[0].At,
		})
	}
	for n, tr := range i.History {
		iv := Interval{State: tr.To, EnteredAt: tr.At}
		if n+1 < len(i.History) {
			iv.LeftAt = i.History[n+1].At
		}
		out = append(out, iv)
	}
	return out
}

// Boundary is a milestone-boundary ticket, reduced to what the milestone
// windows are computed from.
type Boundary struct {
	Ticket    string
	Milestone string
	CreatedAt time.Time
	// Zero while the boundary is still open.
	CompletedAt time.Time
}

// Milestone is one milestone's window and the boundary pass that closed
// it. WindowEnd and BoundaryOpenEnd are zero while it is open.
type Milestone struct {
	Name              string
	Kind              string
	WindowStart       time.Time
	WindowEnd         time.Time
	BoundaryTicket    string
	BoundaryOpenStart time.Time
	BoundaryOpenEnd   time.Time
}

// Milestones derives each milestone's window from the boundary tickets.
//
// Milestones are serial and each has exactly one boundary ticket, so a
// milestone runs from the previous boundary's completion to its own.
//
// **The boundary ticket's own creation is not the milestone's start**,
// and this is the part that costs a wrong answer if assumed: a boundary
// ticket is created when the milestone is ready to *close*. Catapult's
// ORC-90 was created 08-21 00:16 while the engine work it closes runs
// from 08-18, so a window anchored to creation would omit most of the
// work it is meant to measure.
//
// The first milestone has no predecessor, so it opens at `earliest` —
// the creation of the oldest ticket in the project. Every project has
// exactly one such gap and it is not an error.
//
// Ordering is by completion rather than by creation, because that is
// what "serial" means here: an open boundary sorts last whatever its
// creation time, since nothing can follow a milestone that has not ended.
func Milestones(boundaries []Boundary, earliest time.Time) []Milestone {
	if len(boundaries) == 0 {
		return nil
	}
	b := append([]Boundary(nil), boundaries...)
	sort.SliceStable(b, func(x, y int) bool {
		bx, by := b[x], b[y]
		switch {
		case bx.CompletedAt.IsZero() && by.CompletedAt.IsZero():
			return bx.CreatedAt.Before(by.CreatedAt)
		case bx.CompletedAt.IsZero():
			return false
		case by.CompletedAt.IsZero():
			return true
		}
		return bx.CompletedAt.Before(by.CompletedAt)
	})

	out := make([]Milestone, 0, len(b))
	start := earliest
	for _, bd := range b {
		out = append(out, Milestone{
			Name:              bd.Milestone,
			Kind:              KindOf(bd.Milestone),
			WindowStart:       start,
			WindowEnd:         bd.CompletedAt,
			BoundaryTicket:    bd.Ticket,
			BoundaryOpenStart: bd.CreatedAt,
			BoundaryOpenEnd:   bd.CompletedAt,
		})
		// An open boundary ends nothing, so it cannot start the next
		// window either. Nothing follows it anyway — it sorted last.
		if !bd.CompletedAt.IsZero() {
			start = bd.CompletedAt
		}
	}
	return out
}

// KindOf classifies a milestone by its name.
func KindOf(name string) string {
	if strings.HasPrefix(name, DebtPrefix) {
		return KindDebt
	}
	return KindFeature
}

// Earliest is the creation time of the oldest issue, which is where the
// first milestone's window opens.
func Earliest(issues []Issue) time.Time {
	var out time.Time
	for _, i := range issues {
		if out.IsZero() || i.CreatedAt.Before(out) {
			out = i.CreatedAt
		}
	}
	return out
}

// BoundariesOf reduces the issues to their boundary tickets.
func BoundariesOf(issues []Issue) []Boundary {
	var out []Boundary
	for _, i := range issues {
		if !i.IsBoundary() {
			continue
		}
		out = append(out, Boundary{
			Ticket:      i.Key,
			Milestone:   i.Milestone,
			CreatedAt:   i.CreatedAt,
			CompletedAt: i.CompletedAt,
		})
	}
	return out
}

// BillableMillis is what a set of jobs costs, by the host's own model:
// each job rounded up to the whole minute, then summed.
//
// Computed rather than read, because the host's own `billable` figure
// came back zero on every run measured — 4-to-12-minute runs across six
// days, so not settlement lag:
//
//	34013563903  run_duration_ms 445000  billable 0
//	33431474175  run_duration_ms 249000  billable 0
//	33429431365  run_duration_ms 729000  billable 0
//
// Rounding is per job and not per run, which is the difference that
// matters: the sweep is many short jobs, and a 24-second job bills a
// whole minute. Summing durations first and rounding once would hide
// exactly the waste this figure exists to expose.
func BillableMillis(jobs []time.Duration) int64 {
	const minute = int64(time.Minute / time.Millisecond)
	var out int64
	for _, d := range jobs {
		ms := d.Milliseconds()
		if ms <= 0 {
			// A job that never started still bills a minute; one that
			// reports a negative span is the host contradicting itself
			// and is treated the same rather than subtracting.
			out += minute
			continue
		}
		out += ((ms + minute - 1) / minute) * minute
	}
	return out
}
