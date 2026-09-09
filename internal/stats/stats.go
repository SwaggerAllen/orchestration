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
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
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
	// FromCategory is the type of the state moved away from, needed
	// only for the first transition — the one that names the state the
	// ticket was created in.
	FromCategory string
	// Category is the tracker's own type for the state entered
	// (`started`, `completed`, `canceled`, `duplicate`, …). Carried
	// because the default aggregate excludes terminal states, and doing
	// that by category rather than by name is what makes it survive a
	// state nobody here named: Linear's built-in Duplicate has no
	// protocol slug at all, and a future terminal state would have none
	// either.
	Category string
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
	// CurrentCategory is that state's tracker type, for the same reason
	// Transition.Category exists.
	CurrentCategory string
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
	Category  string
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
		return []Interval{{State: i.CurrentState, Category: i.CurrentCategory, EnteredAt: i.CreatedAt}}
	}
	out := make([]Interval, 0, len(i.History)+1)
	if from := i.History[0].From; from != "" {
		out = append(out, Interval{
			State: from,
			// The state a ticket was CREATED in has no category of its
			// own in the history — `fromState` names it, and only the
			// state entered carries a type. Left empty rather than
			// guessed; the default aggregate excludes it by name
			// (backlog, todo) which is where these land anyway.
			Category:  i.History[0].FromCategory,
			EnteredAt: i.CreatedAt,
			LeftAt:    i.History[0].At,
		})
	}
	for n, tr := range i.History {
		iv := Interval{State: tr.To, Category: tr.Category, EnteredAt: tr.At}
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

// StateNamer maps the tracker's display names onto protocol slugs.
//
// It exists because the two halves of this system name states
// differently and nothing forced them to agree. The tracker returns
// display names — "Designing", "Design review", "In Progress" — while
// every constant that reasons about states, here and in the store, is a
// protocol slug. Storing display names would make the store's agent
// total match nothing and read zero forever, which is a flat line rather
// than a failure: neither side's tests can catch it, because the store's
// tests seed their own names and the adapter's assert what Linear
// returns.
//
// Display names are also per-project. `pipeline.config.json` maps each
// protocol state to whatever that team calls it, so two projects can
// disagree about the words while meaning the same state. Slugs are the
// only stable key.
type StateNamer map[string]string

// NewStateNamer inverts the config's protocol-state → tracker-name map.
func NewStateNamer(states map[protocol.State]string) StateNamer {
	n := make(StateNamer, len(states))
	for slug, name := range states {
		n[strings.ToLower(strings.TrimSpace(name))] = string(slug)
	}
	return n
}

// Slug returns the protocol slug for a tracker state name.
//
// A state the pipeline does not own — Linear's built-in Triage and
// Duplicate, or anything the author added — has no slug, and is
// normalised rather than dropped: lower-cased with spaces folded to
// underscores, so "Duplicate" becomes "duplicate" and stays one key
// across every project. Dropping it would lose real time; leaving the
// display name would make it a second naming convention in the same
// column.
func (n StateNamer) Slug(trackerName string) string {
	if trackerName == "" {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(trackerName))
	if slug, ok := n[key]; ok {
		return slug
	}
	return strings.ReplaceAll(key, " ", "_")
}

// Normalise rewrites an issue's state names into protocol slugs.
//
// Applied before the intervals are derived, so everything downstream —
// the store's columns, its agent-state list, the dashboard's filters —
// speaks one vocabulary.
func (n StateNamer) Normalise(i Issue) Issue {
	i.CurrentState = n.Slug(i.CurrentState)
	h := make([]Transition, len(i.History))
	for k, tr := range i.History {
		tr.From = n.Slug(tr.From)
		tr.To = n.Slug(tr.To)
		h[k] = tr
	}
	i.History = h
	return i
}

// OpenBoundaries names the boundary tickets that have not closed.
//
// Milestones are strictly serial, so this should never return more than
// one: a second boundary opening while the first is live means two
// milestones are ending at once, which the pipeline has no idea how to
// mean. Milestones() does not fail on it — it gives both the same window
// start, which is to say overlapping windows — so the collector reports
// the anomaly rather than letting the overlap pass as data.
//
// Handled because it is cheap to handle, not because it is expected.
func OpenBoundaries(boundaries []Boundary) []string {
	var out []string
	for _, b := range boundaries {
		if b.CompletedAt.IsZero() {
			out = append(out, b.Ticket)
		}
	}
	return out
}

// Ticket is one ticket as the store keeps it: the tracker's own facts
// plus the intervals derived from its history.
//
// The milestone the tracker assigns is deliberately absent, and its
// absence is the design rather than an omission. Milestones are matched
// by window (Milestones above), because harness tickets and bugs are
// frequently assigned to no milestone at all and they are exactly the
// work a per-milestone cost question is about. Storing the assignment
// beside the window would offer a second answer to one question.
type Ticket struct {
	Key       string
	Title     string
	Labels    []string
	Priority  int
	CreatedAt time.Time
	// Zero when the ticket has not reached that end.
	CompletedAt time.Time
	CanceledAt  time.Time
	IsBoundary  bool
	Archived    bool
	Intervals   []Interval
}

// Tickets shapes normalised issues into the records the store keeps.
//
// Call it on issues a StateNamer has already normalised. It does not
// normalise them itself: doing so here would make the step optional,
// and skipping it is silent — display names would land in the state
// column, the store's agent-state list would match none of them, and
// the collective agent figure would read zero forever rather than fail.
func Tickets(issues []Issue) []Ticket {
	out := make([]Ticket, 0, len(issues))
	for _, i := range issues {
		out = append(out, Ticket{
			Key:         i.Key,
			Title:       i.Title,
			Labels:      i.Labels,
			Priority:    i.Priority,
			CreatedAt:   i.CreatedAt,
			CompletedAt: i.CompletedAt,
			CanceledAt:  i.CanceledAt,
			IsBoundary:  i.IsBoundary(),
			Archived:    i.Archived(),
			Intervals:   Intervals(i),
		})
	}
	return out
}

// ArchivedCount is how many of these issues the tracker has archived.
//
// Reported by the collector on every pass, because it is the only
// evidence that the enumeration asked for archived issues at all.
// Omitting `includeArchived` does not fail — it silently drops the
// oldest tickets, which renders as a downward trend on every
// per-milestone chart with nothing about the output looking wrong. A
// zero here on a project known to hold archived tickets is that bug,
// visible in the job log rather than in a chart six weeks later.
func ArchivedCount(issues []Issue) int {
	var n int
	for _, i := range issues {
		if i.Archived() {
			n++
		}
	}
	return n
}

// Run is a collected run plus the one fact the host cannot supply:
// whether it is the collector's own.
type Run struct {
	host.StatsRun
	// IsStatsJob marks a run of the workflow that does the collecting.
	//
	// Marked rather than dropped, so runaway spend by the thing
	// measuring spend stays visible. The general rule is that nothing
	// is filtered on the way in: collection is the irreversible step
	// and filtering is free at read time, so a fact excluded at write
	// is a question that can never be asked again. The read side
	// excludes these by default and takes `statsJob=1` to include them.
	IsStatsJob bool
}

// MarkSelf tags the collector's own runs by the workflow file they came
// from. An empty selfWorkflow marks nothing, which is the honest answer
// for a caller that cannot say which workflow it is.
func MarkSelf(runs []host.StatsRun, selfWorkflow string) []Run {
	out := make([]Run, 0, len(runs))
	for _, r := range runs {
		out = append(out, Run{StatsRun: r, IsStatsJob: selfWorkflow != "" && r.Workflow == selfWorkflow})
	}
	return out
}
