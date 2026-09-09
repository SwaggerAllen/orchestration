package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/stats"
	"github.com/SwaggerAllen/orchestration/internal/statsstore"
)

// The bill is not one repository's. The pipeline repo and every project
// it drives spend the same account's minutes, so a collector that only
// ever read the repository it runs in would answer the cost question
// with a fraction of the cost.
func TestReposToCollectTakesAListAndFallsBackToTheJobsOwn(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "swaggerallen/orchestration")
	got, err := reposToCollect(" swaggerallen/catapult , swaggerallen/orchestration ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "swaggerallen/catapult" || got[1] != "swaggerallen/orchestration" {
		t.Errorf("repos = %v", got)
	}
	got, err = reposToCollect("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "swaggerallen/orchestration" {
		t.Errorf("fallback = %v, want the job's own repository", got)
	}
	if _, err := reposToCollect("catapult"); err == nil {
		t.Error("a bare name was accepted; the host needs owner/name")
	}
}

func TestReposToCollectRefusesWithNothingToRead(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	if _, err := reposToCollect(""); err == nil {
		t.Error("collecting from nowhere was accepted")
	}
}

// fakeLister is a host that returns one page of finished runs.
type fakeLister struct{ runs []host.StatsRun }

func (f *fakeLister) ListRunsPage(context.Context, int) ([]host.StatsRun, bool, error) {
	return f.runs, false, nil
}
func (f *fakeLister) RunJobDurations(context.Context, int64) ([]time.Duration, error) {
	return []time.Duration{90 * time.Second}, nil
}

// recorder captures the order the store is written in, which is the
// whole of what collectRepo decides.
type recorder struct {
	order  []string
	runs   []map[string]any
	source string
}

func (rec *recorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.order = append(rec.order, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/runs") {
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Runs []map[string]any `json:"runs"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("unparseable runs body: %v", err)
			}
			rec.runs = body.Runs
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, `{"received":1,"inserted":1}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/watermark") {
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Source string `json:"source"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("unparseable watermark body: %v", err)
			}
			rec.source = body.Source
		}
		_, _ = io.WriteString(w, "{}")
	}))
}

// THE ORDERING THAT CANNOT BE GOT WRONG. The watermark goes down after
// the rows it claims, never before. A watermark ahead of its rows makes
// the collector step over runs it never stored — and those runs age out
// of the host's API while the store is insert-only, so nothing can ever
// fill the gap. Every other failure here is a re-run away from fixed.
func TestTheWatermarkIsWrittenAfterTheRunsItCovers(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t)
	defer srv.Close()

	store := statsstore.NewWorker(srv.URL, "catapult", "tok")
	lister := &fakeLister{runs: []host.StatsRun{
		{ID: 11, Repo: "swaggerallen/catapult", Workflow: "ci.yml", StartedAt: time.Now(), Complete: true},
	}}
	got, err := collectRepo(context.Background(), store, "swaggerallen/catapult", lister,
		nil, 10, "pipeline-stats.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/stats/catapult/runs", "/stats/catapult/watermark"}
	if len(rec.order) != 2 || rec.order[0] != want[0] || rec.order[1] != want[1] {
		t.Fatalf("store was written in the order %v, want %v", rec.order, want)
	}
	if got.Put.Inserted != 1 {
		t.Errorf("inserted = %d, want 1", got.Put.Inserted)
	}
	if got.After.NewestSeen.IsZero() {
		t.Error("the watermark did not advance over the run it collected")
	}
	// And under the key the reader will look for. Asserted here because
	// the probe that collapsed this to a shared "runs" key passed: the
	// order of the writes was pinned and the key they landed under was
	// not.
	if rec.source != runSource("swaggerallen/catapult") {
		t.Errorf("watermark stored under %q, want %q", rec.source, runSource("swaggerallen/catapult"))
	}
	if rec.source == "runs" {
		t.Error("every repository would share one watermark row")
	}
}

// The mark is read under one key and written under another only if the
// two spellings drift, and drift does not fail: the write lands where
// nothing reads, every pass restarts from zero, and re-pays the host's
// rate limit for runs the store already holds — reported as many
// collected and none new, which is what a correctly working
// insert-only store also looks like.
func TestTheWatermarkIsReadAndWrittenUnderOneKey(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t)
	defer srv.Close()
	store := statsstore.NewWorker(srv.URL, "catapult", "tok")

	const repo = "swaggerallen/catapult"
	stored := stats.Watermark{NewestSeen: time.Now(), OldestComplete: time.Now().Add(-time.Hour)}
	marks := map[string]stats.Watermark{runSource(repo): stored}

	got, err := collectRepo(context.Background(), store, repo, &fakeLister{}, marks, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Before.NewestSeen.Equal(stored.NewestSeen) {
		t.Errorf("the pass started from %v, not the stored mark %v", got.Before.NewestSeen, stored.NewestSeen)
	}
	if rec.source != runSource(repo) {
		t.Errorf("wrote under %q, read from %q", rec.source, runSource(repo))
	}
}

// The per-repository watermark is what keeps two repositories from
// telling each other they are caught up: they are separate lists,
// walked separately, and the busier one's forward edge means nothing
// about the quieter one.
func TestEachRepositoryGetsItsOwnWatermarkSource(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t)
	defer srv.Close()
	store := statsstore.NewWorker(srv.URL, "catapult", "tok")

	marks := map[string]stats.Watermark{
		"runs:swaggerallen/catapult": {NewestSeen: time.Now(), OldestComplete: time.Now().Add(-time.Hour)},
	}
	// The other repository's mark is absent, which the collector reads
	// as "collect everything" — the first pass for that source.
	got, err := collectRepo(context.Background(), store, "swaggerallen/orchestration",
		&fakeLister{}, marks, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Before.NewestSeen.IsZero() {
		t.Errorf("orchestration started from %v; it has no mark of its own", got.Before.NewestSeen)
	}
}

// A run of the collecting workflow is stored marked rather than
// dropped, so runaway spend by the thing measuring spend stays
// visible. The read side excludes them by default.
func TestTheCollectorsOwnRunsAreStoredMarked(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t)
	defer srv.Close()
	store := statsstore.NewWorker(srv.URL, "catapult", "tok")

	lister := &fakeLister{runs: []host.StatsRun{
		{ID: 1, Repo: "r", Workflow: "pipeline-stats.yml", StartedAt: time.Now(), Complete: true},
	}}
	if _, err := collectRepo(context.Background(), store, "r", lister,
		nil, 10, "pipeline-stats.yml"); err != nil {
		t.Fatal(err)
	}
	if len(rec.runs) != 1 {
		t.Fatalf("the collector's own run was not sent (%d runs)", len(rec.runs))
	}
	if rec.runs[0]["is_stats_job"] != true {
		t.Errorf("is_stats_job = %#v, want true", rec.runs[0]["is_stats_job"])
	}
}
