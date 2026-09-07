package stats

import (
	"context"
	"fmt"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/host"
)

// RunLister is the host surface the run collector needs. Declared here
// rather than added to the host port: this is the only consumer, and a
// consumer-defined interface keeps the fake it is tested against honest
// about what is actually called.
type RunLister interface {
	// ListRunsPage returns one page of runs, newest first, and whether
	// another page follows.
	ListRunsPage(ctx context.Context, page int) ([]host.StatsRun, bool, error)
	// RunJobDurations returns each job's wall clock for one run.
	RunJobDurations(ctx context.Context, runID int64) ([]time.Duration, error)
}

// Watermark is how far a previous pass got, in both directions.
//
// Two marks rather than one, because the collector walks a list that
// grows at the head while it is reading toward the tail. NewestSeen is
// the forward edge — runs above it are new since the last pass.
// OldestComplete is the backfill frontier: everything between the two is
// already stored, and everything below it is still to fetch.
//
// The pair is what makes a backfill survivable. Catapult holds 5,634
// runs and the jobs call is per-run, so a full sweep exceeds the host's
// hourly rate limit; the collector has to be able to stop mid-walk and
// resume without either re-paying for what it has or skipping what it
// has not.
type Watermark struct {
	NewestSeen     time.Time
	OldestComplete time.Time
}

// CollectResult is one pass's work.
type CollectResult struct {
	// Runs carry their computed minutes and are ready to store.
	Runs []host.StatsRun
	// Watermark is where the next pass should resume. Advanced only
	// over runs actually collected, so a pass that stopped early
	// resumes where it stopped rather than where it was aiming.
	Watermark Watermark
	PagesRead int
	JobCalls  int
	// Exhausted reports that the budget ran out with pages remaining —
	// the backfill is unfinished and the next pass has work to do. Not
	// an error: it is the normal shape of a backfill spanning nights.
	Exhausted bool
	// Skipped counts runs the watermark said were already stored. Worth
	// reporting: a pass that skips everything and collects nothing is
	// either caught up or reading a watermark that is wrong, and the
	// two look identical from the outside.
	Skipped int
	// Incomplete counts runs still in flight, which are not collected.
	Incomplete int
}

// CollectRuns walks the host's run list and computes each new run's
// billable minutes.
//
// ONE WALK, ONE RULE. A run is wanted when it is newer than the forward
// edge or older than the backfill frontier; everything in between is
// already stored. That single predicate serves both the nightly pass
// (which finds a handful above the edge and stops) and the backfill
// (which grinds below the frontier a budget at a time), so there is no
// second code path to drift.
//
// PAGE-BASED RESUME OVER-READS AND NEVER SKIPS. The list is ordered
// newest-first and grows at the head, so a run's page number rises as
// newer runs arrive. Re-walking from page 1 therefore re-lists ground
// already covered — cheap, one call per hundred runs — but cannot step
// over anything. The expensive call is per-run, and the watermark
// predicate is what keeps it off the runs already held.
//
// A partial result is still a result. When the budget runs out, or the
// host refuses mid-walk, everything collected so far is returned with a
// watermark that covers exactly it — so the caller stores the work
// rather than discarding it and paying for it again.
func CollectRuns(ctx context.Context, l RunLister, wm Watermark, budget int) (CollectResult, error) {
	res := CollectResult{Watermark: wm}
	if budget <= 0 {
		return res, nil
	}
	newest, oldest := wm.NewestSeen, wm.OldestComplete

	for page := 1; ; page++ {
		runs, more, err := l.ListRunsPage(ctx, page)
		if err != nil {
			return commit(res, newest, oldest), fmt.Errorf("stats: listing runs page %d: %w", page, err)
		}
		res.PagesRead++
		for _, r := range runs {
			if !r.Complete {
				// Not collected, and — just as important — the forward
				// edge is not advanced past it. Storing it now would
				// freeze a zero the insert-only store could never
				// correct; moving the edge over it would mean no later
				// pass ever looks again.
				res.Incomplete++
				continue
			}
			if !wanted(r.StartedAt, wm) {
				res.Skipped++
				continue
			}
			if res.JobCalls >= budget {
				res.Exhausted = true
				return commit(res, newest, oldest), nil
			}
			durations, err := l.RunJobDurations(ctx, r.ID)
			res.JobCalls++
			if err != nil {
				return commit(res, newest, oldest), fmt.Errorf("stats: jobs for run %d: %w", r.ID, err)
			}
			r.JobCount = len(durations)
			var elapsed time.Duration
			for _, d := range durations {
				elapsed += d
			}
			r.DurationMS = elapsed.Milliseconds()
			r.BillableMS = BillableMillis(durations)
			res.Runs = append(res.Runs, r)

			if newest.IsZero() || r.StartedAt.After(newest) {
				newest = r.StartedAt
			}
			if oldest.IsZero() || r.StartedAt.Before(oldest) {
				oldest = r.StartedAt
			}
		}
		if !more {
			return commit(res, newest, oldest), nil
		}
	}
}

// wanted reports whether a run still needs collecting: above the forward
// edge, or below the backfill frontier. A zero watermark wants
// everything, which is the first pass.
func wanted(started time.Time, wm Watermark) bool {
	if wm.NewestSeen.IsZero() || wm.OldestComplete.IsZero() {
		return true
	}
	return started.After(wm.NewestSeen) || started.Before(wm.OldestComplete)
}

func commit(res CollectResult, newest, oldest time.Time) CollectResult {
	res.Watermark = Watermark{NewestSeen: newest, OldestComplete: oldest}
	return res
}
