package stats

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// T0 anchors the run fixtures in collect_test.go.
var T0 = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

func at(day, hour int) time.Time {
	return time.Date(2026, 8, day, hour, 0, 0, 0, time.UTC)
}

// The shape Linear actually returns, taken from Catapult's ORC-45: a
// first interval from creation in the state the first transition moved
// away from, then one per transition, the last left open.
func TestIntervalsSpanFromCreationToTheOpenEnd(t *testing.T) {
	i := Issue{
		Key:       "ORC-45",
		CreatedAt: at(16, 16),
		History: []Transition{
			{From: "Todo", To: "In Progress", At: at(16, 17)},
			{From: "In Progress", To: "Blocked", At: at(16, 18)},
			{From: "Blocked", To: "Done", At: at(16, 21)},
		},
	}
	got := Intervals(i)
	want := []Interval{
		{State: "Todo", EnteredAt: at(16, 16), LeftAt: at(16, 17)},
		{State: "In Progress", EnteredAt: at(16, 17), LeftAt: at(16, 18)},
		{State: "Blocked", EnteredAt: at(16, 18), LeftAt: at(16, 21)},
		{State: "Done", EnteredAt: at(16, 21)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d intervals, want %d: %+v", len(got), len(want), got)
	}
	for n := range want {
		if got[n] != want[n] {
			t.Errorf("interval %d = %+v, want %+v", n, got[n], want[n])
		}
	}
}

// The interval history cannot state on its own. Without it a ticket's
// first state is invisible and every duration before its first move is
// lost -- which is the whole of Todo and Backlog time.
func TestTheFirstIntervalStartsAtCreationNotAtTheFirstMove(t *testing.T) {
	i := Issue{
		CreatedAt: at(12, 9),
		History:   []Transition{{From: "Backlog", To: "Todo", At: at(18, 21)}},
	}
	got := Intervals(i)
	if len(got) != 2 {
		t.Fatalf("got %d intervals, want 2: %+v", len(got), got)
	}
	if got[0].State != "Backlog" {
		t.Errorf("first interval is %q, want the state the ticket was created in", got[0].State)
	}
	if !got[0].EnteredAt.Equal(at(12, 9)) {
		t.Errorf("first interval starts at %v, want creation %v", got[0].EnteredAt, at(12, 9))
	}
	if d := got[0].Duration(); d != 6*24*time.Hour+12*time.Hour {
		t.Errorf("backlog span is %v, want 6d12h", d)
	}
}

func TestATicketThatNeverMovedHasOneOpenInterval(t *testing.T) {
	i := Issue{CreatedAt: at(20, 1), CurrentState: "Triage"}
	got := Intervals(i)
	if len(got) != 1 || got[0].State != "Triage" || !got[0].LeftAt.IsZero() {
		t.Fatalf("got %+v, want one open Triage interval", got)
	}
	if Intervals(Issue{CreatedAt: at(20, 1)}) != nil {
		t.Error("an issue with no history and no current state invented an interval")
	}
}

// Rework instances are counted by entry, so a state entered three times
// must produce three intervals rather than one merged span.
func TestARepeatedStateProducesOneIntervalPerEntry(t *testing.T) {
	i := Issue{
		CreatedAt: at(3, 0),
		History: []Transition{
			{From: "Designing", To: "Ready for rework", At: at(3, 1)},
			{From: "Ready for rework", To: "In Progress", At: at(3, 2)},
			{From: "In Progress", To: "Ready for rework", At: at(3, 3)},
			{From: "Ready for rework", To: "Done", At: at(3, 4)},
		},
	}
	var rework int
	for _, iv := range Intervals(i) {
		if iv.State == "Ready for rework" {
			rework++
		}
	}
	if rework != 2 {
		t.Errorf("counted %d rework intervals, want 2", rework)
	}
}

// An open interval is zero rather than "now minus entered", so the same
// query answered twice cannot disagree with itself.
func TestAnOpenIntervalHasNoDuration(t *testing.T) {
	iv := Interval{State: "In Progress", EnteredAt: at(1, 0)}
	if d := iv.Duration(); d != 0 {
		t.Errorf("an open interval measured %v, want 0", d)
	}
}

// ---- milestones ---------------------------------------------------

func boundary(ticket, milestone string, created, completed time.Time) Boundary {
	return Boundary{Ticket: ticket, Milestone: milestone, CreatedAt: created, CompletedAt: completed}
}

// Catapult's real chain, reduced. The window runs boundary-close to
// boundary-close; the first opens at the project's earliest ticket.
func TestMilestoneWindowsChainFromCloseToClose(t *testing.T) {
	got := Milestones([]Boundary{
		boundary("ORC-28", "Hookup", at(14, 16), at(14, 20)),
		boundary("ORC-45", DebtPrefix+"before the engine", at(16, 16), at(18, 21)),
		boundary("ORC-90", "The engine", at(21, 0), at(21, 23)),
	}, at(12, 18))

	if len(got) != 3 {
		t.Fatalf("got %d milestones, want 3", len(got))
	}
	if !got[0].WindowStart.Equal(at(12, 18)) {
		t.Errorf("the first window opens at %v, want the earliest ticket %v", got[0].WindowStart, at(12, 18))
	}
	if !got[1].WindowStart.Equal(at(14, 20)) {
		t.Errorf("window 1 opens at %v, want the previous boundary's close %v", got[1].WindowStart, at(14, 20))
	}
	if !got[2].WindowStart.Equal(at(18, 21)) {
		t.Errorf("window 2 opens at %v, want %v", got[2].WindowStart, at(18, 21))
	}
	if !got[2].WindowEnd.Equal(at(21, 23)) {
		t.Errorf("window 2 closes at %v, want %v", got[2].WindowEnd, at(21, 23))
	}
}

// THE one that costs a wrong answer if assumed. ORC-90 was created
// 08-21 while the engine work it closes runs from 08-18, because a
// boundary ticket is created when the milestone is ready to CLOSE.
// Anchoring the window to creation omits most of the work it measures.
func TestTheWindowIgnoresWhenTheBoundaryTicketWasCreated(t *testing.T) {
	got := Milestones([]Boundary{
		boundary("ORC-45", DebtPrefix+"before the engine", at(16, 16), at(18, 21)),
		boundary("ORC-90", "The engine", at(21, 0), at(21, 23)),
	}, at(12, 0))

	engine := got[1]
	if engine.WindowStart.Equal(at(21, 0)) {
		t.Fatal("the window opened at the boundary ticket's creation; three days of engine work fall outside it")
	}
	if !engine.WindowStart.Equal(at(18, 21)) {
		t.Errorf("window opens at %v, want the previous boundary's close %v", engine.WindowStart, at(18, 21))
	}
	// The creation time is still carried -- it is when the boundary
	// pass itself began, which is a different question.
	if !engine.BoundaryOpenStart.Equal(at(21, 0)) {
		t.Errorf("boundary open start is %v, want the ticket's creation %v", engine.BoundaryOpenStart, at(21, 0))
	}
}

func TestAnOpenBoundaryLeavesItsWindowOpenAndSortsLast(t *testing.T) {
	got := Milestones([]Boundary{
		// Deliberately out of order, and the open one created BEFORE
		// the closed one completed -- so creation order and completion
		// order disagree.
		boundary("ORC-217", "Product tier", at(3, 0), time.Time{}),
		boundary("ORC-156", DebtPrefix+"before the product tier", at(30, 16), at(1, 2).AddDate(0, 1, 0)),
	}, at(12, 0))

	if len(got) != 2 {
		t.Fatalf("got %d milestones, want 2", len(got))
	}
	if got[1].Name != "Product tier" {
		t.Errorf("the open boundary sorted %d, want last", 0)
	}
	if !got[1].WindowEnd.IsZero() {
		t.Errorf("an open boundary closed its window at %v", got[1].WindowEnd)
	}
	if !got[1].BoundaryOpenEnd.IsZero() {
		t.Error("an open boundary reported a boundary-open end")
	}
}

// Completion is the sort key, not creation, and nothing exercised that
// until this test: every other fixture happens to have boundaries whose
// creation order already matches their completion order, so swapping the
// comparison changed no answer.
//
// The divergence is easy to produce by hand. File the next milestone's
// boundary ticket while the current one is still open -- entirely normal
// -- and the earlier-created boundary is the later-completed one. Sorting
// by creation then chains the windows backwards, and the second window
// ends BEFORE it starts, which no consumer checks for.
func TestBoundariesSortByCompletionEvenWhenCreatedOutOfOrder(t *testing.T) {
	got := Milestones([]Boundary{
		// Created first, closed last: filed early, sat open while the
		// other milestone was closed out.
		boundary("ORC-A", "Filed early, closed late", at(1, 0), at(10, 0)),
		boundary("ORC-B", "Filed late, closed early", at(2, 0), at(5, 0)),
	}, at(0, 0))

	if len(got) != 2 {
		t.Fatalf("got %d milestones, want 2", len(got))
	}
	if got[0].Name != "Filed late, closed early" {
		t.Fatalf("first milestone is %q; boundaries sorted by creation, not completion", got[0].Name)
	}
	if !got[1].WindowStart.Equal(at(5, 0)) {
		t.Errorf("second window opens at %v, want the first boundary's close %v", got[1].WindowStart, at(5, 0))
	}
	for n, m := range got {
		if !m.WindowEnd.IsZero() && m.WindowEnd.Before(m.WindowStart) {
			t.Errorf("milestone %d (%q) ends %v before it starts %v", n, m.Name, m.WindowEnd, m.WindowStart)
		}
	}
}

func TestNoBoundariesYieldsNoMilestones(t *testing.T) {
	if got := Milestones(nil, at(1, 0)); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestTechDebtMilestonesAreClassifiedByTitle(t *testing.T) {
	for name, want := range map[string]string{
		DebtPrefix + "before the engine":       KindDebt,
		DebtPrefix + "before the product tier": KindDebt,
		"The engine":                           KindFeature,
		"Product tier and intake":              KindFeature,
		// Not the separator the tracker uses. A hyphen is a different
		// milestone name and must not be swept in by accident.
		"Tech debt - before the engine": KindFeature,
	} {
		if got := KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBoundariesAndEarliestComeOffTheIssues(t *testing.T) {
	issues := []Issue{
		{Key: "ORC-7", CreatedAt: at(12, 18)},
		{Key: "ORC-45", CreatedAt: at(16, 16), CompletedAt: at(18, 21),
			Milestone: DebtPrefix + "before the engine", Labels: []string{core.LabelBoundary}},
		{Key: "ORC-23", CreatedAt: at(13, 1)},
	}
	if got := Earliest(issues); !got.Equal(at(12, 18)) {
		t.Errorf("Earliest = %v, want %v", got, at(12, 18))
	}
	b := BoundariesOf(issues)
	if len(b) != 1 || b[0].Ticket != "ORC-45" {
		t.Fatalf("BoundariesOf = %+v, want just ORC-45", b)
	}
	if !b[0].CompletedAt.Equal(at(18, 21)) {
		t.Errorf("boundary completion = %v, want %v", b[0].CompletedAt, at(18, 21))
	}
}

// Archived tickets are the oldest ones, so dropping them renders as a
// downward trend with nothing in the output looking wrong.
func TestAnArchivedIssueIsStillAnIssue(t *testing.T) {
	i := Issue{Key: "ORC-23", CreatedAt: at(13, 1), ArchivedAt: at(14, 0),
		History: []Transition{{From: "Todo", To: "Done", At: at(13, 5)}}}
	if !i.Archived() {
		t.Error("an issue with an archivedAt did not report as archived")
	}
	if len(Intervals(i)) != 2 {
		t.Error("an archived issue's history was not read")
	}
	if got := Earliest([]Issue{i}); !got.Equal(at(13, 1)) {
		t.Error("an archived issue was skipped when finding the earliest")
	}
}

// ---- billable minutes ---------------------------------------------

// The host's own billable figure is zero on every run measured, so this
// is the number the whole cost question rests on.
func TestBillableRoundsEachJobUpToTheMinute(t *testing.T) {
	for _, c := range []struct {
		name string
		jobs []time.Duration
		want int64
	}{
		{"a 24-second sweep bills a whole minute", []time.Duration{24 * time.Second}, 60_000},
		{"exactly one minute bills one", []time.Duration{time.Minute}, 60_000},
		{"a second over bills two", []time.Duration{61 * time.Second}, 120_000},
		{"a 7m25s agent run bills eight", []time.Duration{445 * time.Second}, 480_000},
		{"three short jobs bill three minutes", []time.Duration{24 * time.Second, 24 * time.Second, 24 * time.Second}, 180_000},
		{"a job with no measured span still bills a minute", []time.Duration{0}, 60_000},
		{"a negative span is not subtracted", []time.Duration{-5 * time.Second}, 60_000},
		{"no jobs bill nothing", nil, 0},
	} {
		if got := BillableMillis(c.jobs); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// Rounding per JOB rather than per run is the difference that exposes
// the waste. Summing first and rounding once hides it exactly where the
// sweep spends it.
func TestRoundingIsPerJobNotPerRun(t *testing.T) {
	jobs := []time.Duration{24 * time.Second, 24 * time.Second, 24 * time.Second}
	perJob := BillableMillis(jobs)
	perRun := BillableMillis([]time.Duration{72 * time.Second})
	if perJob == perRun {
		t.Fatal("three 24s jobs billed the same as one 72s run; per-job rounding is not happening")
	}
	if perJob != 180_000 || perRun != 120_000 {
		t.Errorf("per-job %d (want 180000), per-run %d (want 120000)", perJob, perRun)
	}
	// The waste is the gap between billed and elapsed.
	if waste := perJob - 72_000; waste != 108_000 {
		t.Errorf("rounding waste = %d, want 108000", waste)
	}
}

// ---- state naming ---------------------------------------------------

// The tracker says "Design review" and every constant that reasons
// about states says `design_review`. Storing display names would make
// the store's agent-state list match nothing and report zero agent time
// forever — a flat line rather than a failure, invisible to both sides'
// tests.
func TestTrackerDisplayNamesBecomeProtocolSlugs(t *testing.T) {
	n := NewStateNamer(map[protocol.State]string{
		protocol.Designing:    "Designing",
		protocol.DesignReview: "Design review",
		protocol.InProgress:   "In Progress",
	})
	for name, want := range map[string]string{
		"Designing":     "designing",
		"Design review": "design_review",
		"In Progress":   "in_progress",
		"in progress":   "in_progress", // case is the tracker's, not ours
		" Designing ":   "designing",
	} {
		if got := n.Slug(name); got != want {
			t.Errorf("Slug(%q) = %q, want %q", name, got, want)
		}
	}
}

// THE test that proves the config map is load-bearing, and nothing did
// until it existed.
//
// Every state Catapult uses is named exactly its slug with spaces for
// underscores, so the fallback normaliser produces the right answer for
// all of them — with the map inverted, or absent, or wrong. A probe
// that reversed the map printed ok against a suite that only used
// those.
//
// The map earns its place on the states a project names differently,
// which is the entire reason `pipeline.config.json` carries one: the
// tracker's words are per-team and the slug is the only stable key.
func TestAStateNamedUnlikeItsSlugStillResolves(t *testing.T) {
	n := NewStateNamer(map[protocol.State]string{
		protocol.ReadyForRework: "Rework queue",
		protocol.Merged:         "Shipped",
		protocol.Designing:      "Designing",
	})
	for name, want := range map[string]string{
		"Rework queue": "ready_for_rework",
		"Shipped":      "merged",
		"Designing":    "designing",
	} {
		if got := n.Slug(name); got != want {
			t.Errorf("Slug(%q) = %q, want %q — the config map is not being read, "+
				"only the fallback that lower-cases and underscores", name, got, want)
		}
	}
}

// Triage and Duplicate are Linear's own and have no protocol slug.
// Dropping them would lose real time; leaving the display name would put
// two naming conventions in one column.
func TestAStateThePipelineDoesNotOwnIsNormalisedNotDropped(t *testing.T) {
	n := NewStateNamer(map[protocol.State]string{protocol.Designing: "Designing"})
	for name, want := range map[string]string{
		"Duplicate":      "duplicate",
		"Triage":         "triage",
		"Waiting on Bob": "waiting_on_bob",
		"":               "",
	} {
		if got := n.Slug(name); got != want {
			t.Errorf("Slug(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestNormaliseRewritesTheWholeHistory(t *testing.T) {
	n := NewStateNamer(map[protocol.State]string{
		protocol.Todo:       "Todo",
		protocol.Designing:  "Designing",
		protocol.InProgress: "In Progress",
	})
	got := n.Normalise(Issue{
		CurrentState: "In Progress",
		History: []Transition{
			{From: "Todo", To: "Designing", At: at(1, 0)},
			{From: "Designing", To: "In Progress", At: at(1, 1)},
		},
	})
	if got.CurrentState != "in_progress" {
		t.Errorf("current state = %q", got.CurrentState)
	}
	want := []Transition{
		{From: "todo", To: "designing", At: at(1, 0)},
		{From: "designing", To: "in_progress", At: at(1, 1)},
	}
	for k := range want {
		if got.History[k].From != want[k].From || got.History[k].To != want[k].To {
			t.Errorf("transition %d = %q->%q, want %q->%q", k,
				got.History[k].From, got.History[k].To, want[k].From, want[k].To)
		}
	}
}

// The whole point: what Normalise produces must be what the store's
// agent-state list matches. These two lists living in different
// languages is the defect this exists to prevent, so the test names both
// ends rather than trusting either.
func TestTheNormalisedAgentStatesAreTheOnesTheStoreCounts(t *testing.T) {
	n := NewStateNamer(map[protocol.State]string{
		protocol.Designing:    "Designing",
		protocol.DesignReview: "Design review",
		protocol.InProgress:   "In Progress",
		protocol.Reconciling:  "Reconciling",
		protocol.Checks:       "Checks",
	})
	// Mirrors AGENT_STATES in worker/projectstats.ts.
	for tracker, slug := range map[string]string{
		"Designing":     "designing",
		"Design review": "design_review",
		"In Progress":   "in_progress",
		"Reconciling":   "reconciling",
		"Checks":        "checks",
	} {
		if got := n.Slug(tracker); got != slug {
			t.Errorf("the store counts %q but the collector would write %q for %q", slug, got, tracker)
		}
	}
}

// ---- Murphy -------------------------------------------------------

// Milestones are strictly serial, so two open boundaries should never
// happen. Milestones() gives both the same window start — overlapping
// windows — so the anomaly is reported rather than passing as data.
func TestTwoOpenBoundariesAreReported(t *testing.T) {
	bs := []Boundary{
		boundary("ORC-156", "Debt", at(1, 0), at(5, 0)),
		boundary("ORC-217", "Product tier", at(6, 0), time.Time{}),
		boundary("ORC-260", "Next thing", at(7, 0), time.Time{}),
	}
	open := OpenBoundaries(bs)
	if len(open) != 2 {
		t.Fatalf("OpenBoundaries = %v, want both open tickets", open)
	}
	// And the overlap it is warning about is real, not theoretical.
	ms := Milestones(bs, at(0, 0))
	if !ms[1].WindowStart.Equal(ms[2].WindowStart) {
		t.Error("the two open boundaries did not in fact overlap; the warning would be describing nothing")
	}
}

func TestOneOpenBoundaryIsNormalAndReportsAsOne(t *testing.T) {
	open := OpenBoundaries([]Boundary{
		boundary("ORC-156", "Debt", at(1, 0), at(5, 0)),
		boundary("ORC-217", "Product tier", at(6, 0), time.Time{}),
	})
	if len(open) != 1 || open[0] != "ORC-217" {
		t.Errorf("OpenBoundaries = %v, want [ORC-217]", open)
	}
}

// Tickets carries the two flags nothing downstream can re-derive: the
// boundary label is the milestone table's whole source, and archived is
// the only evidence the enumeration asked for archived issues at all.
func TestTicketsCarryTheBoundaryAndArchivedFlags(t *testing.T) {
	created := time.Date(2026, 8, 21, 0, 16, 0, 0, time.UTC)
	issues := []Issue{
		{
			Key: "ORC-90", Labels: []string{core.LabelBoundary}, Milestone: "The engine",
			CreatedAt: created, ArchivedAt: created.Add(72 * time.Hour),
			CurrentState: "done", CurrentCategory: "completed",
		},
		{Key: "ORC-91", CreatedAt: created, CurrentState: "todo"},
	}
	got := Tickets(issues)
	if len(got) != 2 {
		t.Fatalf("got %d tickets, want 2", len(got))
	}
	if !got[0].IsBoundary || !got[0].Archived {
		t.Errorf("ORC-90: boundary=%v archived=%v, want both true", got[0].IsBoundary, got[0].Archived)
	}
	if got[1].IsBoundary || got[1].Archived {
		t.Errorf("ORC-91: boundary=%v archived=%v, want both false", got[1].IsBoundary, got[1].Archived)
	}
	// The intervals ride along, because the store replaces a ticket's
	// intervals wholesale from this document — a ticket sent without
	// them arrives having deleted its own history.
	if len(got[0].Intervals) == 0 {
		t.Error("ORC-90 was shaped with no intervals; the store would replace its history with nothing")
	}
	if n := ArchivedCount(issues); n != 1 {
		t.Errorf("ArchivedCount = %d, want 1", n)
	}
}

// The milestone the tracker assigns is deliberately not carried: windows
// decide milestone membership, because the harness tickets and bugs a
// cost question is about are frequently assigned to none. A second
// answer to one question is the failure being avoided.
func TestTicketsDoNotCarryTheTrackersMilestoneAssignment(t *testing.T) {
	got := Tickets([]Issue{{Key: "ORC-1", Milestone: "The engine", CurrentState: "done"}})
	if v := fmt.Sprintf("%+v", got[0]); strings.Contains(v, "The engine") {
		t.Errorf("the shaped ticket carries a milestone assignment: %s", v)
	}
}

// The collector's own runs are marked, never dropped: nothing is
// filtered on the way in, because collection is the irreversible step
// and filtering is free at read time. Runaway spend by the thing
// measuring spend has to stay visible.
func TestMarkSelfTagsTheCollectorsOwnRunsWithoutDroppingThem(t *testing.T) {
	runs := []host.StatsRun{
		{ID: 1, Workflow: "pipeline-stats.yml"},
		{ID: 2, Workflow: "ci.yml"},
	}
	got := MarkSelf(runs, "pipeline-stats.yml")
	if len(got) != 2 {
		t.Fatalf("MarkSelf returned %d runs, want both — nothing is filtered at collection", len(got))
	}
	if !got[0].IsStatsJob || got[1].IsStatsJob {
		t.Errorf("marked = %v, %v; want the stats workflow marked and ci.yml not", got[0].IsStatsJob, got[1].IsStatsJob)
	}
	// An empty name marks nothing rather than everything: a caller that
	// cannot say which workflow it is overstates the bill, which is the
	// safe direction.
	for _, r := range MarkSelf(runs, "") {
		if r.IsStatsJob {
			t.Errorf("run %d was marked with no self-workflow named", r.ID)
		}
	}
}
