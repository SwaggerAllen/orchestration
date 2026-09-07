package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// route answers each path with a fixed body and records the paths asked
// for, so a test can assert what the collector actually requested.
func route(t *testing.T, bodies map[string]string) (*Client, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		for k, v := range bodies {
			if strings.HasPrefix(r.URL.RequestURI(), k) {
				_, _ = w.Write([]byte(v))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c, err := New("swaggerallen/dummy", "gh_test", WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c, &asked
}

const runsPage = `{"total_count":3,"workflow_runs":[
 {"id":1,"name":"CI","display_title":"Some commit","path":".github/workflows/ci.yml",
  "status":"completed","conclusion":"success","run_attempt":1,
  "run_started_at":"2026-09-06T05:16:15Z","created_at":"2026-09-06T05:16:10Z"},
 {"id":2,"name":"agent","display_title":"pipeline: dev ORC-233","path":".github/workflows/pipeline-agent-dev.yml",
  "status":"completed","conclusion":"failure","run_attempt":2,
  "run_started_at":"2026-09-06T05:20:00Z","created_at":"2026-09-06T05:19:00Z"},
 {"id":3,"name":"queued","display_title":"pipeline: design ORC-234","path":".github/workflows/pipeline-agent-design.yml",
  "status":"in_progress","conclusion":null,"run_attempt":1,
  "run_started_at":null,"created_at":"2026-09-06T05:25:00Z"}
]}`

// Most minutes belong to runs that name no ticket. Dropping them would
// measure a minority of the bill and call it the bill.
func TestARunWithNoTicketIsStillListed(t *testing.T) {
	c, _ := route(t, map[string]string{"/repos/swaggerallen/dummy/actions/runs": runsPage})
	got, _, err := c.ListRunsPage(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d runs, want 3", len(got))
	}
	ci := got[0]
	if ci.TicketKey != "" || ci.Kind != "" {
		t.Errorf("ci.yml correlated to a ticket: %+v", ci)
	}
	if ci.Workflow != "ci.yml" {
		t.Errorf("workflow = %q, want ci.yml", ci.Workflow)
	}
	if ci.Repo != "swaggerallen/dummy" {
		t.Errorf("repo = %q", ci.Repo)
	}
}

// The same regexp the sweep correlates with, so a run counted here and
// a run dispatched there cannot disagree about which ticket it is.
func TestAnAgentRunCarriesItsKindAndTicket(t *testing.T) {
	c, _ := route(t, map[string]string{"/repos/swaggerallen/dummy/actions/runs": runsPage})
	got, _, _ := c.ListRunsPage(context.Background(), 1)
	dev := got[1]
	if dev.Kind != "dev" || dev.TicketKey != "ORC-233" {
		t.Errorf("kind/ticket = %q/%q, want dev/ORC-233", dev.Kind, dev.TicketKey)
	}
	if dev.Attempt != 2 {
		t.Errorf("attempt = %d, want 2 — a re-run keeps the id, so the attempt is what separates them", dev.Attempt)
	}
	if dev.Conclusion != "failure" {
		t.Errorf("conclusion = %q, want failure", dev.Conclusion)
	}
}

// The workflow FILE, not the run name: run-name is per-ticket by design
// ("pipeline: dev ORC-233"), so grouping minutes by it gives one series
// per ticket instead of one per workflow.
func TestMinutesGroupByTheWorkflowFileNotTheRunName(t *testing.T) {
	c, _ := route(t, map[string]string{"/repos/swaggerallen/dummy/actions/runs": runsPage})
	got, _, _ := c.ListRunsPage(context.Background(), 1)
	if got[1].Workflow != "pipeline-agent-dev.yml" {
		t.Errorf("workflow = %q, want the file", got[1].Workflow)
	}
	if got[1].Workflow == got[1].Name {
		t.Error("the workflow is the run name; minutes would split one series per ticket")
	}
}

// An unfinished run's minutes are not yet knowable, and the store is
// insert-only -- so a zero written now is a zero no later pass can fix.
func TestAnUnfinishedRunIsMarkedIncomplete(t *testing.T) {
	c, _ := route(t, map[string]string{"/repos/swaggerallen/dummy/actions/runs": runsPage})
	got, _, _ := c.ListRunsPage(context.Background(), 1)
	if !got[0].Complete || !got[1].Complete {
		t.Error("a completed run was not marked complete")
	}
	if got[2].Complete {
		t.Error("an in-progress run was marked complete; its zero minutes would be frozen by the insert-only store")
	}
	// A queued run has no start yet; creation keeps the row bucketable.
	if got[2].StartedAt.IsZero() {
		t.Error("a run with no run_started_at got no start at all")
	}
	if want := time.Date(2026, 9, 6, 5, 25, 0, 0, time.UTC); !got[2].StartedAt.Equal(want) {
		t.Errorf("started = %v, want the creation time %v", got[2].StartedAt, want)
	}
}

// "More pages" is arithmetic against total_count. Reading it off a full
// page instead would make an exactly-full LAST page look like there is
// another, and the backfill would ask forever.
func TestMorePagesComesFromTheTotalNotThePageLength(t *testing.T) {
	full := `{"total_count":100,"workflow_runs":[` + strings.Repeat(
		`{"id":1,"display_title":"x","path":".github/workflows/ci.yml","status":"completed","run_started_at":"2026-09-06T05:00:00Z"},`, 99) +
		`{"id":100,"display_title":"x","path":".github/workflows/ci.yml","status":"completed","run_started_at":"2026-09-06T05:00:00Z"}]}`
	c, _ := route(t, map[string]string{"/repos/swaggerallen/dummy/actions/runs": full})
	got, more, err := c.ListRunsPage(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Fatalf("got %d runs, want a full page of 100", len(got))
	}
	if more {
		t.Error("an exactly-full last page reported another page; the backfill would never stop")
	}
}

func TestPagingAsksForTheRequestedPage(t *testing.T) {
	c, asked := route(t, map[string]string{"/repos/swaggerallen/dummy/actions/runs": runsPage})
	if _, _, err := c.ListRunsPage(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*asked)[0], "page=3") || !strings.Contains((*asked)[0], "per_page=100") {
		t.Errorf("asked %q, want page=3 at per_page=100", (*asked)[0])
	}
}

// ---- jobs ----------------------------------------------------------

const jobsBody = `{"total_count":2,"jobs":[
 {"status":"completed","started_at":"2026-09-06T05:16:15Z","completed_at":"2026-09-06T05:16:39Z"},
 {"status":"completed","started_at":"2026-09-06T05:16:15Z","completed_at":"2026-09-06T05:23:40Z"}
]}`

func TestJobDurationsAreReadPerJob(t *testing.T) {
	c, _ := route(t, map[string]string{"/repos/swaggerallen/dummy/actions/runs/7/jobs": jobsBody})
	got, err := c.RunJobDurations(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d durations, want 2", len(got))
	}
	if got[0] != 24*time.Second {
		t.Errorf("job 0 = %v, want 24s", got[0])
	}
	if got[1] != 445*time.Second {
		t.Errorf("job 1 = %v, want 445s (the measured design run)", got[1])
	}
}

// A short job list is an understated bill with nothing saying so.
func TestATruncatedJobListIsRefused(t *testing.T) {
	c, _ := route(t, map[string]string{
		"/repos/swaggerallen/dummy/actions/runs/7/jobs": `{"total_count":150,"jobs":[
		 {"status":"completed","started_at":"2026-09-06T05:16:15Z","completed_at":"2026-09-06T05:16:39Z"}]}`,
	})
	_, err := c.RunJobDurations(context.Background(), 7)
	if err == nil {
		t.Fatal("a truncated job list was accepted; the run's minutes would be understated")
	}
	if !strings.Contains(err.Error(), "150") {
		t.Errorf("the error does not say how many were missed: %v", err)
	}
}

// Counting an unfinished job as zero would bill a minute for time it
// has not spent. Skipped instead; a later pass sees it finished.
func TestAnUnfinishedJobIsSkippedRatherThanCountedAsZero(t *testing.T) {
	c, _ := route(t, map[string]string{
		"/repos/swaggerallen/dummy/actions/runs/7/jobs": `{"total_count":2,"jobs":[
		 {"status":"completed","started_at":"2026-09-06T05:16:15Z","completed_at":"2026-09-06T05:16:39Z"},
		 {"status":"in_progress","started_at":"2026-09-06T05:16:15Z","completed_at":null}]}`,
	})
	got, err := c.RunJobDurations(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d durations, want 1 — the running job must not be counted", len(got))
	}
}
