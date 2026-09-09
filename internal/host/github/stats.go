package github

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/host"
)

// runsPageSize is GitHub's maximum for the runs listing. The backfill
// reads thousands of runs — Catapult alone holds 5,634 — so the page
// size is the difference between fifty-seven list calls and five hundred.
const runsPageSize = 100

// ListRunsPage reads one page of the repository's Actions runs,
// newest first, and reports whether another page follows.
//
// One page rather than "all of them", because the caller owns the
// stopping rule: the backfill spans more runs than GitHub's hourly rate
// limit allows in one sitting, so it must be able to stop mid-walk and
// resume from a watermark. Putting the loop here would bury that.
//
// Runs are returned whether or not their name correlates to a ticket.
// `ci.yml`, worker deploys and preview builds match no agent run name
// and are plausibly most of the minutes spent — dropping them would
// measure a minority of the bill and call it the bill.
func (c *Client) ListRunsPage(ctx context.Context, page int) ([]host.StatsRun, bool, error) {
	if page < 1 {
		page = 1
	}
	var data struct {
		TotalCount   int `json:"total_count"`
		WorkflowRuns []struct {
			ID           int64     `json:"id"`
			Name         string    `json:"name"`
			DisplayTitle string    `json:"display_title"`
			Path         string    `json:"path"`
			Status       string    `json:"status"`
			Conclusion   string    `json:"conclusion"`
			RunAttempt   int       `json:"run_attempt"`
			RunStartedAt time.Time `json:"run_started_at"`
			CreatedAt    time.Time `json:"created_at"`
		} `json:"workflow_runs"`
	}
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=%d&page=%d", c.owner, c.repo, runsPageSize, page)
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return nil, false, err
	}
	out := make([]host.StatsRun, 0, len(data.WorkflowRuns))
	for _, r := range data.WorkflowRuns {
		started := r.RunStartedAt
		if started.IsZero() {
			// A queued run has no start yet. Creation is the closest
			// honest answer and keeps the row bucketable; its duration
			// stays zero until a later pass sees it finished.
			started = r.CreatedAt
		}
		sr := host.StatsRun{
			ID:         r.ID,
			Repo:       c.owner + "/" + c.repo,
			Workflow:   workflowFileOf(r.Path),
			Name:       r.DisplayTitle,
			StartedAt:  started,
			Conclusion: r.Conclusion,
			Attempt:    r.RunAttempt,
			Complete:   r.Status == "completed",
		}
		// Kind and ticket come from the same regexp the sweep
		// correlates with, so a run counted here and a run dispatched
		// there cannot disagree about which ticket they belong to.
		if m := RunNameFields(r.DisplayTitle); m != nil {
			// RunNameFields returns the two capture groups, not the
			// full submatch — index 0 is the kind, not the whole name.
			sr.Kind, sr.TicketKey = m[0], m[1]
		}
		out = append(out, sr)
	}
	// GitHub reports the total rather than a next-page flag, so "more"
	// is arithmetic. Reading it off `len(page) == perPage` instead would
	// make an exactly-full last page look like there is another.
	more := page*runsPageSize < data.TotalCount
	return out, more, nil
}

// workflowFileOf reduces `.github/workflows/ci.yml` to `ci.yml`.
//
// The file rather than the run's `name`, because the name is whatever
// `run-name:` evaluated to and the agent stubs deliberately make that
// per-ticket ("pipeline: dev ORC-233"). Grouping minutes by it would
// produce one series per ticket instead of one per workflow.
func workflowFileOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// RunJobDurations reads each job's wall clock for one run.
//
// This is where billable minutes come from, and it is a per-run call
// because GitHub offers no bulk jobs endpoint — which is the whole
// reason the backfill needs a budget and a watermark.
//
// The host's own `billable` figure is not used: it read zero on every
// catapult run measured, across six days, while the durations were
// accurate. So minutes are reconstructed from these spans, per job and
// rounded up, which is the host's documented billing model anyway.
func (c *Client) RunJobDurations(ctx context.Context, runID int64) ([]time.Duration, error) {
	var data struct {
		TotalCount int `json:"total_count"`
		Jobs       []struct {
			StartedAt   time.Time `json:"started_at"`
			CompletedAt time.Time `json:"completed_at"`
			Status      string    `json:"status"`
		} `json:"jobs"`
	}
	// per_page at the maximum: a run with more than 100 jobs would
	// otherwise be silently short, and a short job list is an
	// understated bill with nothing saying so.
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=%d", c.owner, c.repo, runID, runsPageSize)
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return nil, err
	}
	if data.TotalCount > len(data.Jobs) {
		return nil, fmt.Errorf("github: run %d has %d jobs but only %d were returned — refusing to understate its minutes",
			runID, data.TotalCount, len(data.Jobs))
	}
	out := make([]time.Duration, 0, len(data.Jobs))
	for _, j := range data.Jobs {
		if j.Status != "completed" || j.StartedAt.IsZero() || j.CompletedAt.IsZero() {
			// Still running, or never ran. Skipped rather than counted
			// as zero: a zero would bill a minute for a job that has
			// not spent one, and the next pass will see it finished.
			continue
		}
		out = append(out, j.CompletedAt.Sub(j.StartedAt))
	}
	return out, nil
}
