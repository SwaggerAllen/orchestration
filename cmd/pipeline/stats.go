package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/host/github"
	"github.com/SwaggerAllen/orchestration/internal/stats"
	"github.com/SwaggerAllen/orchestration/internal/statsstore"
	"github.com/SwaggerAllen/orchestration/internal/tracker/linear"
)

func cmdStats(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("stats: want a subcommand: collect")
	}
	switch args[0] {
	case "collect":
		return cmdStatsCollect(args[1:])
	default:
		return fmt.Errorf("stats: unknown subcommand %q", args[0])
	}
}

// defaultRunBudget bounds the per-repository jobs calls one pass makes.
//
// A BOUND, NOT A MEASUREMENT. The jobs endpoint is one call per run and
// GitHub documents 5,000 authenticated requests per hour, so a full
// backfill of Catapult's 5,634 runs cannot happen in one sitting
// whatever this is set to — which is why the watermark exists and why
// running out is a normal outcome rather than an error. 400 per
// repository leaves most of the hour's allowance to everything else the
// pipeline does, and a backfill that takes a week of nights costs
// nothing because the runs are not going anywhere.
const defaultRunBudget = 400

// defaultSelfWorkflow is the workflow this collector runs from.
//
// Its runs are marked rather than dropped, so runaway spend by the
// thing measuring spend stays visible. A wrong value here is not a
// failure — it marks nothing, and the collector's own minutes then read
// as ordinary minutes, which overstates the bill rather than hiding it.
const defaultSelfWorkflow = "pipeline-stats.yml"

// cmdStatsCollect recomputes the tracker half and advances the run half
// (DESIGN §13).
//
// ONE PASS DOES BOTH HALVES, AND THEY ARE WRITTEN DIFFERENTLY. Linear's
// archive is a visibility flag rather than a deletion, so tickets and
// milestones are a full recompute and idempotent upsert, safe to rebuild
// forever. GitHub's run data ages out, so runs are insert-only and the
// store has no route that removes one. Running this again is therefore
// always safe, which is the property the backfill depends on.
//
// WHAT IT DELIBERATELY DOES NOT DO: cross-check the month's computed
// minutes against the host's own billing total. That endpoint is
// account-scoped and no credential this runs under can read it — not
// the in-workflow GITHUB_TOKEN, not a repository-scoped fine-grained
// PAT. So the pass prints its own month total instead and the
// comparison against Settings → Billing is a human's glance. Building
// the call would mean building one that fails in exactly the place the
// collector lives.
func cmdStatsCollect(args []string) error {
	fs := flag.NewFlagSet("stats collect", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	repoList := fs.String("repos", "", "comma-separated owner/name repositories to collect runs from; defaults to $GITHUB_REPOSITORY")
	budget := fs.Int("budget", defaultRunBudget, "per-repository cap on jobs calls in one pass")
	selfWorkflow := fs.String("self-workflow", defaultSelfWorkflow, "workflow file this collector runs from; its runs are marked rather than dropped")
	skipRuns := fs.Bool("skip-runs", false, "recompute the tracker half only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("stats collect: LINEAR_API_KEY is not set")
	}
	store := statsstore.NewWorker(cfg.State.URL, cfg.State.Project, os.Getenv("PIPELINE_STATE_TOKEN"))
	if store == nil {
		// Refused rather than warned, unlike the move record's client.
		// A pass with nowhere to write spends the host's rate limit and
		// throws the result away, and the allowance does not come back
		// for an hour.
		return fmt.Errorf("stats collect: no stats store configured — set state.url and state.project in %s and PIPELINE_STATE_TOKEN in the environment", *cfgPath)
	}
	ctx := context.Background()

	// ---- the tracker half ------------------------------------------
	issues, err := linear.New(apiKey).ListIssuesForStats(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		return err
	}
	namer := stats.NewStateNamer(cfg.States)
	normalised := make([]stats.Issue, 0, len(issues))
	for _, i := range issues {
		normalised = append(normalised, namer.Normalise(i))
	}
	boundaries := stats.BoundariesOf(normalised)
	milestones := stats.Milestones(boundaries, stats.Earliest(normalised))
	if err := store.PutMilestones(ctx, milestones); err != nil {
		return err
	}
	tickets := stats.Tickets(normalised)
	if err := store.PutTickets(ctx, tickets); err != nil {
		return err
	}
	archived := stats.ArchivedCount(normalised)
	open := stats.OpenBoundaries(boundaries)

	fmt.Printf("tickets: %d upserted, %d archived; milestones: %d\n", len(tickets), archived, len(milestones))
	if archived == 0 {
		// Not an error — a young project genuinely has none. Named
		// because the alternative cause is the query having dropped
		// `includeArchived`, which does not fail: it silently
		// under-counts the oldest milestones and renders as a downward
		// trend with nothing about the output looking wrong.
		fmt.Println("  no archived tickets were returned — expected on a young project, and the shape of a lost includeArchived on an old one")
	}
	if len(open) > 1 {
		// Milestones are strictly serial, so this cannot mean anything.
		// Milestones() does not fail on it — it gives both the same
		// window start, which is to say overlapping windows — so the
		// anomaly is reported here rather than left to pass as data.
		fmt.Printf("  WARNING: %d boundary tickets are open at once (%s); milestones are serial, so their windows now overlap\n",
			len(open), strings.Join(open, ", "))
	}

	// ---- the run half ----------------------------------------------
	var collected []repoResult
	if !*skipRuns {
		repos, err := reposToCollect(*repoList)
		if err != nil {
			return err
		}
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			return fmt.Errorf("stats collect: GITHUB_TOKEN is not set; pass --skip-runs to recompute the tracker half alone")
		}
		marks, err := store.Watermarks(ctx)
		if err != nil {
			return err
		}
		for _, repo := range repos {
			gh, err := github.New(repo, token)
			if err != nil {
				return err
			}
			r, err := collectRepo(ctx, store, repo, gh, marks, *budget, *selfWorkflow)
			collected = append(collected, r)
			if err != nil {
				// The partial result is already stored — CollectRuns
				// returns what it got alongside the failure, and the
				// watermark covers exactly that. Reporting and stopping
				// beats discarding work the rate limit was spent on.
				report(tickets, archived, milestones, open, collected, nil)
				return err
			}
		}
	}

	month := monthTotal(ctx, store)
	report(tickets, archived, milestones, open, collected, month)
	return nil
}

// repoResult is one repository's pass, for the report.
type repoResult struct {
	Repo   string
	Before stats.Watermark
	After  stats.Watermark
	Result stats.CollectResult
	Put    statsstore.PutResult
}

// runSource names a repository's watermark row.
//
// It has exactly one call site, and that is deliberate: collectRepo
// takes the whole map and does its own lookup, so the key it reads and
// the key it writes cannot be different strings. They used to be — the
// caller looked the mark up and collectRepo wrote it — and two
// spellings that drift do not fail. The write lands under a key nothing
// reads, every pass restarts from a zero watermark, re-pays the host's
// rate limit for runs already stored, and reports them all as collected
// and none as new. Which is also what the insert-only store looks like
// when it is working.
//
// Both halves were found by probe. Collapsing the key to a shared
// "runs" passed the ordering test, which pinned the order of the writes
// and not the key; pinning the key then left the caller's own lookup
// uncovered, because nothing exercises cmdStatsCollect. Removing the
// second derivation was cheaper than covering it.
func runSource(repo string) string { return "runs:" + repo }

// collectRepo walks one repository and stores what it got.
//
// The lister is passed in rather than built here so the store
// choreography below — rows first, watermark second — is exercisable
// without a host. The whole watermark map is passed in for the reason
// runSource gives.
//
// One watermark per repository, because they are separate lists walked
// separately. Sharing one would have the busier repository's forward
// edge tell the quieter one it was caught up.
func collectRepo(ctx context.Context, store *statsstore.Worker, repo string, lister stats.RunLister,
	marks map[string]stats.Watermark, budget int, selfWorkflow string) (repoResult, error) {

	source := runSource(repo)
	before := marks[source]
	out := repoResult{Repo: repo, Before: before}

	res, collectErr := stats.CollectRuns(ctx, lister, before, budget)
	out.Result = res
	out.After = res.Watermark

	if len(res.Runs) > 0 {
		put, err := store.PutRuns(ctx, stats.MarkSelf(res.Runs, selfWorkflow))
		if err != nil {
			return out, err
		}
		out.Put = put
	}
	// Written after the rows, never before. A watermark ahead of the
	// rows it claims is the one unrecoverable ordering here: the runs it
	// skipped past age out of the host's API, and the store is
	// insert-only, so nothing can ever fill the gap.
	if err := store.PutWatermark(ctx, source, res.Watermark); err != nil {
		return out, err
	}
	fmt.Printf("%s: %d run(s) collected (%d new), %d skipped, %d in flight, %d page(s), %d jobs call(s)%s\n",
		repo, len(res.Runs), out.Put.Inserted, res.Skipped, res.Incomplete,
		res.PagesRead, res.JobCalls, exhaustedNote(res.Exhausted))
	return out, collectErr
}

func exhaustedNote(exhausted bool) string {
	if !exhausted {
		return ""
	}
	return " — budget spent with pages remaining; the next pass resumes here"
}

// reposToCollect reads the repository list, falling back to the one the
// job is running in.
//
// The list is explicit because the bill is not one repository's. The
// pipeline's own repo and every project it drives spend the same
// account's minutes, and measuring one of them would answer the cost
// question with a fraction of the cost.
func reposToCollect(list string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		if repo := os.Getenv("GITHUB_REPOSITORY"); repo != "" {
			return []string{repo}, nil
		}
		return nil, fmt.Errorf("stats collect: no repositories — pass --repos owner/name,owner/name or set GITHUB_REPOSITORY")
	}
	var out []string
	for _, r := range strings.Split(list, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !strings.Contains(r, "/") {
			return nil, fmt.Errorf("stats collect: repository %q is not owner/name", r)
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("stats collect: --repos named no repositories")
	}
	return out, nil
}

// monthTotal reads back this calendar month's stored minutes.
//
// Read from the store rather than summed from this pass, and that is the
// point: the pass collects an arbitrary slice of history, so its own sum
// is not a month's bill. This is the number to hold against the host's
// billing page, which is the only cross-check the arithmetic has.
//
// A failure here is printed and swallowed. The collection already
// landed, and losing a pass over a read that only informs a human would
// be the tail wagging the dog.
func monthTotal(ctx context.Context, store *statsstore.Worker) []statsstore.MinuteRow {
	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	rows, err := store.Minutes(ctx, from, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats collect: reading this month's minutes back: %v\n", err)
		return nil
	}
	return rows
}

func totals(rows []statsstore.MinuteRow) (billable, duration, rounding int64, runs int) {
	for _, r := range rows {
		billable += r.BillableMS
		duration += r.DurationMS
		rounding += r.RoundingMS
		runs += r.Runs
	}
	return
}

func minutesOf(ms int64) int64 { return ms / int64(time.Minute/time.Millisecond) }

func report(tickets []stats.Ticket, archived int, milestones []stats.Milestone,
	openBoundaries []string, repos []repoResult, month []statsstore.MinuteRow) {

	summarize(func(w io.Writer) {
		summaryHeading(w, "Pipeline stats")
		fmt.Fprintf(w, "**%d** ticket(s) upserted, **%d** of them archived. **%d** milestone(s).\n\n",
			len(tickets), archived, len(milestones))
		if archived == 0 {
			fmt.Fprint(w, "> No archived tickets came back. Expected on a young project; on an old one it is the shape of an enumeration that lost `includeArchived`, which under-counts the oldest milestones and renders as a downward trend.\n\n")
		}
		if len(openBoundaries) > 1 {
			fmt.Fprintf(w, "> **%d boundary tickets are open at once** (%s). Milestones are serial, so their windows overlap and every per-milestone figure spanning them is wrong until one closes.\n\n",
				len(openBoundaries), strings.Join(openBoundaries, ", "))
		}
		if len(repos) > 0 {
			fmt.Fprintln(w, "| Repository | Collected | New | Skipped | In flight | Jobs calls | Backfill |")
			fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
			for _, r := range repos {
				backfill := "caught up"
				if r.Result.Exhausted {
					backfill = "budget spent, resumes next pass"
				}
				fmt.Fprintf(w, "| `%s` | %d | %d | %d | %d | %d | %s |\n",
					r.Repo, len(r.Result.Runs), r.Put.Inserted, r.Result.Skipped,
					r.Result.Incomplete, r.Result.JobCalls, backfill)
			}
			fmt.Fprint(w, "\nCollected counts what the pass fetched; New counts what the store did not already hold. The run table is insert-only, so a repeated pass reporting many collected and none new is the mechanism working.\n\n")
			for _, r := range repos {
				fmt.Fprintf(w, "- `%s` watermark %s → %s\n", r.Repo, window(r.Before), window(r.After))
			}
			fmt.Fprintln(w)
		}
		if month != nil {
			billable, duration, rounding, runs := totals(month)
			fmt.Fprintf(w, "**This month so far: %d minute(s) across %d run(s)**, of which %d minute(s) are per-job rounding waste (raw wall clock %d minute(s)).\n\n",
				minutesOf(billable), runs, minutesOf(rounding), minutesOf(duration))
			fmt.Fprint(w, "Hold that against Settings → Billing. It is the only cross-check the arithmetic has: account billing is not readable by any credential this runs under, so nothing here can make the comparison for you. A figure that does not land near the host's own means the arithmetic is wrong, not that the host is.\n")
		}
	})

	if month != nil {
		billable, _, rounding, runs := totals(month)
		fmt.Printf("this month: %d minute(s) over %d run(s), %d of them rounding waste — cross-check against Settings → Billing\n",
			minutesOf(billable), runs, minutesOf(rounding))
	}
}

func window(wm stats.Watermark) string {
	if wm.NewestSeen.IsZero() && wm.OldestComplete.IsZero() {
		return "(none)"
	}
	return fmt.Sprintf("[%s … %s]", stamp(wm.OldestComplete), stamp(wm.NewestSeen))
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02")
}
