package stats

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/host"
)

// fakeLister serves pages of runs and records which runs were asked for
// job durations — the expensive call, and the one the watermark exists
// to keep off runs already stored.
type fakeLister struct {
	pages    [][]host.StatsRun
	jobs     map[int64][]time.Duration
	asked    []int64
	pagesGot []int
	failPage int   // 1-based; 0 = never
	failJob  int64 // run id whose jobs call errors; 0 = never
}

func (f *fakeLister) ListRunsPage(_ context.Context, page int) ([]host.StatsRun, bool, error) {
	f.pagesGot = append(f.pagesGot, page)
	if f.failPage == page {
		return nil, false, errors.New("rate limited")
	}
	if page < 1 || page > len(f.pages) {
		return nil, false, nil
	}
	return f.pages[page-1], page < len(f.pages), nil
}

func (f *fakeLister) RunJobDurations(_ context.Context, runID int64) ([]time.Duration, error) {
	f.asked = append(f.asked, runID)
	if f.failJob == runID {
		return nil, errors.New("rate limited")
	}
	if d, ok := f.jobs[runID]; ok {
		return d, nil
	}
	return []time.Duration{24 * time.Second}, nil
}

// run n started n hours after T0, so a higher id is a newer run.
func run(id int64, complete bool) host.StatsRun {
	return host.StatsRun{
		ID: id, Repo: "o/r", Workflow: "ci.yml",
		StartedAt: T0.Add(time.Duration(id) * time.Hour), Complete: complete,
	}
}

func lister(runsPerPage ...[]host.StatsRun) *fakeLister {
	return &fakeLister{pages: runsPerPage, jobs: map[int64][]time.Duration{}}
}

func TestAFirstPassWithNoWatermarkCollectsEverything(t *testing.T) {
	l := lister([]host.StatsRun{run(3, true), run(2, true)}, []host.StatsRun{run(1, true)})
	got, err := CollectRuns(context.Background(), l, Watermark{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 3 {
		t.Fatalf("collected %d runs, want 3", len(got.Runs))
	}
	if !got.Watermark.NewestSeen.Equal(T0.Add(3 * time.Hour)) {
		t.Errorf("newest = %v, want run 3", got.Watermark.NewestSeen)
	}
	if !got.Watermark.OldestComplete.Equal(T0.Add(1 * time.Hour)) {
		t.Errorf("oldest = %v, want run 1", got.Watermark.OldestComplete)
	}
	if got.PagesRead != 2 {
		t.Errorf("read %d pages, want 2", got.PagesRead)
	}
}

// The expensive call is per-run. A run the watermark already covers must
// cost nothing at all, or a nightly pass re-pays for the whole history.
func TestARunAlreadyStoredCostsNoJobsCall(t *testing.T) {
	l := lister([]host.StatsRun{run(5, true), run(4, true), run(3, true), run(2, true), run(1, true)})
	wm := Watermark{NewestSeen: T0.Add(4 * time.Hour), OldestComplete: T0.Add(2 * time.Hour)}
	got, err := CollectRuns(context.Background(), l, wm, 100)
	if err != nil {
		t.Fatal(err)
	}
	// 5 is above the edge, 1 is below the frontier; 2, 3 and 4 are held.
	if len(got.Runs) != 2 {
		t.Fatalf("collected %d runs, want 2 (one new, one backfilled)", len(got.Runs))
	}
	if got.Skipped != 3 {
		t.Errorf("skipped %d, want 3", got.Skipped)
	}
	if len(l.asked) != 2 {
		t.Errorf("made %d jobs calls for %d wanted runs: %v", len(l.asked), 2, l.asked)
	}
	for _, id := range l.asked {
		if id == 2 || id == 3 || id == 4 {
			t.Errorf("paid for run %d, which the watermark already covered", id)
		}
	}
}

// Both directions from one predicate, so the nightly pass and the
// backfill cannot drift into two behaviours.
func TestTheWalkGoesForwardAndBackwardInOnePass(t *testing.T) {
	l := lister([]host.StatsRun{run(9, true), run(5, true), run(1, true)})
	wm := Watermark{NewestSeen: T0.Add(5 * time.Hour), OldestComplete: T0.Add(5 * time.Hour)}
	got, _ := CollectRuns(context.Background(), l, wm, 100)
	var ids []int64
	for _, r := range got.Runs {
		ids = append(ids, r.ID)
	}
	if len(ids) != 2 || ids[0] != 9 || ids[1] != 1 {
		t.Fatalf("collected %v, want the new one (9) and the old one (1)", ids)
	}
	if !got.Watermark.NewestSeen.Equal(T0.Add(9*time.Hour)) || !got.Watermark.OldestComplete.Equal(T0.Add(1*time.Hour)) {
		t.Errorf("watermark = %+v, want both edges moved", got.Watermark)
	}
}

// A backfill spanning nights is the normal shape, not an error.
func TestTheBudgetStopsTheWalkAndSaysSo(t *testing.T) {
	l := lister([]host.StatsRun{run(5, true), run(4, true), run(3, true)}, []host.StatsRun{run(2, true), run(1, true)})
	got, err := CollectRuns(context.Background(), l, Watermark{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exhausted {
		t.Error("the budget ran out and the result did not say so")
	}
	if len(got.Runs) != 2 {
		t.Fatalf("collected %d runs, want exactly the budget of 2", len(got.Runs))
	}
	if got.JobCalls != 2 {
		t.Errorf("made %d jobs calls, want 2", got.JobCalls)
	}
	// The watermark must cover exactly what was collected, so the next
	// pass resumes where this one stopped rather than where it aimed.
	if !got.Watermark.OldestComplete.Equal(T0.Add(4 * time.Hour)) {
		t.Errorf("oldest = %v, want run 4 — the last one actually collected", got.Watermark.OldestComplete)
	}
}

// Resuming must pick up exactly where the budget bit, with nothing
// skipped in between. Two bounded passes must equal one unbounded one.
func TestTwoBoundedPassesCollectWhatOneUnboundedPassWould(t *testing.T) {
	pages := func() [][]host.StatsRun {
		return [][]host.StatsRun{{run(5, true), run(4, true), run(3, true)}, {run(2, true), run(1, true)}}
	}
	all, _ := CollectRuns(context.Background(), lister(pages()...), Watermark{}, 100)

	first, _ := CollectRuns(context.Background(), lister(pages()...), Watermark{}, 2)
	second, _ := CollectRuns(context.Background(), lister(pages()...), first.Watermark, 100)

	seen := map[int64]bool{}
	for _, r := range append(append([]host.StatsRun{}, first.Runs...), second.Runs...) {
		if seen[r.ID] {
			t.Errorf("run %d was collected twice across the resume", r.ID)
		}
		seen[r.ID] = true
	}
	if len(seen) != len(all.Runs) {
		t.Fatalf("two passes collected %d runs, one pass collected %d", len(seen), len(all.Runs))
	}
	for _, r := range all.Runs {
		if !seen[r.ID] {
			t.Errorf("run %d fell through the resume", r.ID)
		}
	}
}

// An unfinished run has no minutes yet, and the store is insert-only —
// so a zero written now can never be corrected. Advancing the forward
// edge past it would be worse: no later pass would look again.
func TestAnUnfinishedRunIsNeitherCollectedNorSteppedOver(t *testing.T) {
	l := lister([]host.StatsRun{run(3, false), run(2, true)})
	got, err := CollectRuns(context.Background(), l, Watermark{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 1 || got.Runs[0].ID != 2 {
		t.Fatalf("collected %+v, want only the finished run", got.Runs)
	}
	if got.Incomplete != 1 {
		t.Errorf("incomplete = %d, want 1", got.Incomplete)
	}
	if got.Watermark.NewestSeen.Equal(T0.Add(3 * time.Hour)) {
		t.Error("the forward edge moved past an unfinished run; no later pass would collect it")
	}
	for _, id := range l.asked {
		if id == 3 {
			t.Error("paid for the jobs of a run still in flight")
		}
	}
}

// The rate limit lands mid-walk by design. Everything already paid for
// must survive it, with a watermark covering exactly that.
func TestAFailureMidWalkKeepsWhatWasAlreadyCollected(t *testing.T) {
	l := lister([]host.StatsRun{run(3, true), run(2, true)}, []host.StatsRun{run(1, true)})
	l.failPage = 2
	got, err := CollectRuns(context.Background(), l, Watermark{}, 100)
	if err == nil {
		t.Fatal("a failing page did not surface an error")
	}
	if len(got.Runs) != 2 {
		t.Fatalf("kept %d runs, want the 2 already paid for", len(got.Runs))
	}
	if !got.Watermark.OldestComplete.Equal(T0.Add(2 * time.Hour)) {
		t.Errorf("oldest = %v, want run 2 — the watermark must cover exactly what survived", got.Watermark.OldestComplete)
	}
}

func TestAFailingJobsCallKeepsTheRunsBeforeIt(t *testing.T) {
	l := lister([]host.StatsRun{run(3, true), run(2, true), run(1, true)})
	l.failJob = 2
	got, err := CollectRuns(context.Background(), l, Watermark{}, 100)
	if err == nil {
		t.Fatal("a failing jobs call did not surface an error")
	}
	if len(got.Runs) != 1 || got.Runs[0].ID != 3 {
		t.Fatalf("kept %+v, want run 3 only", got.Runs)
	}
}

// Minutes are per job and rounded up; elapsed is the raw sum. The gap
// between them is the rounding waste, and it only exists if both are
// carried.
func TestMinutesAreComputedPerJobAndElapsedIsKeptBeside(t *testing.T) {
	l := lister([]host.StatsRun{run(1, true)})
	l.jobs[1] = []time.Duration{24 * time.Second, 24 * time.Second, 24 * time.Second}
	got, err := CollectRuns(context.Background(), l, Watermark{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	r := got.Runs[0]
	if r.BillableMS != 180_000 {
		t.Errorf("billable = %d, want 180000 (three jobs, each rounded up)", r.BillableMS)
	}
	if r.DurationMS != 72_000 {
		t.Errorf("elapsed = %d, want 72000", r.DurationMS)
	}
	if r.JobCount != 3 {
		t.Errorf("job count = %d, want 3", r.JobCount)
	}
	if r.BillableMS-r.DurationMS != 108_000 {
		t.Errorf("rounding waste = %d, want 108000", r.BillableMS-r.DurationMS)
	}
}

// A pass that is caught up and a pass reading a broken watermark both
// collect nothing; only the skip count tells them apart.
func TestACaughtUpPassReportsWhatItSkipped(t *testing.T) {
	l := lister([]host.StatsRun{run(3, true), run(2, true)})
	wm := Watermark{NewestSeen: T0.Add(3 * time.Hour), OldestComplete: T0.Add(2 * time.Hour)}
	got, _ := CollectRuns(context.Background(), l, wm, 100)
	if len(got.Runs) != 0 {
		t.Fatalf("collected %d runs when caught up", len(got.Runs))
	}
	if got.Skipped != 2 {
		t.Errorf("skipped = %d, want 2 — without it, caught-up and broken look identical", got.Skipped)
	}
	if got.Watermark != wm {
		t.Errorf("watermark moved on a pass that collected nothing: %+v", got.Watermark)
	}
}

func TestAZeroBudgetDoesNothingRatherThanEverything(t *testing.T) {
	l := lister([]host.StatsRun{run(1, true)})
	got, err := CollectRuns(context.Background(), l, Watermark{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 0 || got.PagesRead != 0 {
		t.Errorf("a zero budget collected %d runs over %d pages", len(got.Runs), got.PagesRead)
	}
}

// A half-populated watermark is reachable: the store accepts either
// mark as null, so a row written by a pass that was interrupted, or by
// an older shape, can carry a forward edge with no frontier.
//
// It must degrade to a full re-scan. Without the zero check the general
// predicate reads `after(newest) || before(zero)` — the second half is
// false for every real time — so the collector would take the new runs
// each night and never once look below the edge. A backfill that
// silently never starts, on a store whose row counts keep rising.
func TestAHalfWrittenWatermarkRescansRatherThanNeverBackfilling(t *testing.T) {
	pages := []host.StatsRun{run(5, true), run(4, true), run(3, true)}

	// Forward edge only: no frontier to walk down from.
	forwardOnly, err := CollectRuns(context.Background(), lister(pages), Watermark{NewestSeen: T0.Add(4 * time.Hour)}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(forwardOnly.Runs) != 3 {
		t.Errorf("collected %d runs from a frontier-less watermark, want all 3 — "+
			"anything less means the backfill can never start", len(forwardOnly.Runs))
	}

	// Frontier only: no forward edge.
	backOnly, err := CollectRuns(context.Background(), lister(pages), Watermark{OldestComplete: T0.Add(4 * time.Hour)}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(backOnly.Runs) != 3 {
		t.Errorf("collected %d runs from an edge-less watermark, want all 3", len(backOnly.Runs))
	}

	// And the repaired watermark covers both ends, so the next pass is
	// back on the cheap path.
	if forwardOnly.Watermark.OldestComplete.IsZero() || forwardOnly.Watermark.NewestSeen.IsZero() {
		t.Errorf("the re-scan did not repair the watermark: %+v", forwardOnly.Watermark)
	}
}
