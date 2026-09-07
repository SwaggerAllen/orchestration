package statsstore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/stats"
)

// capture records what the client actually put on the wire, which is
// the only thing the store ever sees.
type capture struct {
	paths  []string
	auths  []string
	bodies []map[string]any
	reply  map[string]string
}

func (c *capture) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.paths = append(c.paths, r.Method+" "+r.URL.Path)
		c.auths = append(c.auths, r.Header.Get("authorization"))
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("%s sent unparseable JSON: %v", r.URL.Path, err)
			}
			c.bodies = append(c.bodies, body)
		}
		if reply, ok := c.reply[r.URL.Path]; ok {
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, reply)
			return
		}
		_, _ = io.WriteString(w, "{}")
	}))
}

func client(t *testing.T, c *capture) (*Worker, func()) {
	t.Helper()
	srv := c.server(t)
	return NewWorker(srv.URL, "catapult", "tok"), srv.Close
}

// THE UNIT PIN. Every timestamp the store keeps is epoch milliseconds,
// because the read side buckets with `strftime(fmt, col / 1000,
// 'unixepoch')`. Seconds do not fail: they land every row in 1970 and
// render as an empty chart with one stray bucket at the far left, which
// no test on either side would notice — the store's tests seed their own
// numbers and the derivation's never leave Go.
func TestTimestampsGoOnTheWireInMilliseconds(t *testing.T) {
	c := &capture{}
	w, done := client(t, c)
	defer done()

	created := time.Date(2026, 9, 6, 22, 35, 37, 0, time.UTC)
	left := created.Add(90 * time.Minute)
	err := w.PutTickets(context.Background(), []stats.Ticket{{
		Key: "ORC-233", CreatedAt: created,
		Intervals: []stats.Interval{{State: "designing", EnteredAt: created, LeftAt: left}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ticket := c.bodies[0]["tickets"].([]any)[0].(map[string]any)
	if got, want := int64(ticket["created_at"].(float64)), created.UnixMilli(); got != want {
		t.Errorf("created_at = %d, want %d (milliseconds)", got, want)
	}
	// Spelt out as well as compared, so the magnitude is visible to a
	// reader: 2026-09-06T22:35:37Z is 1788734137000 in milliseconds and
	// 1788734137 in seconds, and it is the ten-digit one that silently
	// lands the row in 1970.
	if got := int64(ticket["created_at"].(float64)); got != 1788734137000 {
		t.Errorf("created_at = %d, want 1788734137000", got)
	}
	iv := ticket["intervals"].([]any)[0].(map[string]any)
	if got, want := int64(iv["left_at"].(float64)), left.UnixMilli(); got != want {
		t.Errorf("interval left_at = %d, want %d", got, want)
	}
}

// A zero time is an absent fact, and the columns it lands in are
// nullable for that reason. Sending 0 does not fail either — it sorts
// as the oldest row in the table, so an unfinished ticket reads as one
// completed at the epoch.
func TestAnUnreachedEndIsNullRatherThanZero(t *testing.T) {
	c := &capture{}
	w, done := client(t, c)
	defer done()

	err := w.PutTickets(context.Background(), []stats.Ticket{{
		Key: "ORC-1", CreatedAt: time.Now(),
		Intervals: []stats.Interval{{State: "checks", EnteredAt: time.Now()}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ticket := c.bodies[0]["tickets"].([]any)[0].(map[string]any)
	for _, field := range []string{"completed_at", "canceled_at"} {
		if v, ok := ticket[field]; !ok || v != nil {
			t.Errorf("%s = %#v, want null for a ticket that has not reached that end", field, v)
		}
	}
	iv := ticket["intervals"].([]any)[0].(map[string]any)
	if v := iv["left_at"]; v != nil {
		t.Errorf("an open interval sent left_at = %#v, want null", v)
	}
}

// The field names are the store's SQL column names, and a rename on
// either side is silent: the store reads `t.key`, `r.run_id` and so on
// off the parsed body, so a mismatch stores a NULL rather than raising.
func TestTheWireNamesTheStoresOwnColumns(t *testing.T) {
	c := &capture{reply: map[string]string{
		"/stats/catapult/runs": `{"received":1,"inserted":1}`,
	}}
	w, done := client(t, c)
	defer done()
	ctx := context.Background()

	now := time.Now()
	if err := w.PutMilestones(ctx, []stats.Milestone{{
		Name: "The engine", Kind: stats.KindFeature, WindowStart: now, BoundaryTicket: "ORC-90",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PutRuns(ctx, []stats.Run{{
		StatsRun: host.StatsRun{ID: 7, Repo: "swaggerallen/catapult", Workflow: "ci.yml", StartedAt: now},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := w.PutWatermark(ctx, "runs:swaggerallen/catapult", stats.Watermark{NewestSeen: now}); err != nil {
		t.Fatal(err)
	}

	milestone := c.bodies[0]["milestones"].([]any)[0].(map[string]any)
	for _, k := range []string{"name", "kind", "window_start", "window_end", "boundary_ticket", "boundary_open_start", "boundary_open_end"} {
		if _, ok := milestone[k]; !ok {
			t.Errorf("milestone body has no %q", k)
		}
	}
	run := c.bodies[1]["runs"].([]any)[0].(map[string]any)
	for _, k := range []string{"run_id", "repo", "workflow", "run_name", "kind", "ticket_key", "started_at", "duration_ms", "billable_ms", "job_count", "conclusion", "attempt", "is_stats_job"} {
		if _, ok := run[k]; !ok {
			t.Errorf("run body has no %q", k)
		}
	}
	for _, k := range []string{"source", "oldest_complete", "newest_seen"} {
		if _, ok := c.bodies[2][k]; !ok {
			t.Errorf("watermark body has no %q", k)
		}
	}
}

// A run that names no ticket is stored with a NULL ticket_key, not an
// empty string. `ci.yml`, worker deploys and preview builds match no
// correlation convention and are plausibly most of the minutes, so they
// are kept — and the read side's joins treat "" as a ticket key that
// simply matches nothing.
func TestATicketlessRunSendsANullTicketKey(t *testing.T) {
	c := &capture{reply: map[string]string{"/stats/catapult/runs": `{"received":1,"inserted":1}`}}
	w, done := client(t, c)
	defer done()
	if _, err := w.PutRuns(context.Background(), []stats.Run{{
		StatsRun: host.StatsRun{ID: 1, Repo: "r", Workflow: "ci.yml", StartedAt: time.Now()},
	}}); err != nil {
		t.Fatal(err)
	}
	run := c.bodies[0]["runs"].([]any)[0].(map[string]any)
	if v := run["ticket_key"]; v != nil {
		t.Errorf("ticket_key = %#v, want null", v)
	}
}

// Every request carries the shared secret and addresses this project's
// object. The path segment is what keeps two projects out of each
// other's rows.
func TestEveryRequestIsAuthorisedAndProjectScoped(t *testing.T) {
	c := &capture{reply: map[string]string{"/stats/catapult/watermark": `[]`}}
	w, done := client(t, c)
	defer done()
	if _, err := w.Watermarks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.paths[0]; got != "GET /stats/catapult/watermark" {
		t.Errorf("path = %q, want the project's stats object", got)
	}
	if got := c.auths[0]; got != "Bearer tok" {
		t.Errorf("authorization = %q", got)
	}
}

// The watermark round-trips through milliseconds, and an absent mark
// comes back as the zero time rather than 1970. The collector reads a
// zero as "collect everything", which is the first pass; reading a
// null as epoch-zero instead would make every later pass believe the
// backfill was finished.
func TestWatermarksComeBackAsTimes(t *testing.T) {
	c := &capture{reply: map[string]string{
		"/stats/catapult/watermark": `[{"source":"runs:a","oldest_complete":1788734137000,"newest_seen":1788737737000},
		                               {"source":"runs:b","oldest_complete":null,"newest_seen":null}]`,
	}}
	w, done := client(t, c)
	defer done()
	got, err := w.Watermarks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := time.UnixMilli(1788734137000).UTC(); !got["runs:a"].OldestComplete.Equal(want) {
		t.Errorf("oldest_complete = %v, want %v", got["runs:a"].OldestComplete, want)
	}
	if !got["runs:b"].NewestSeen.IsZero() {
		t.Errorf("a null mark decoded as %v, want the zero time", got["runs:b"].NewestSeen)
	}
}

// A non-200 is an error carrying the store's own words. The collector
// stops on it rather than advancing a watermark over rows that were
// never written.
func TestAStoreRefusalIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "each run needs run_id, repo and started_at\n")
	}))
	defer srv.Close()
	w := NewWorker(srv.URL, "catapult", "tok")
	_, err := w.PutRuns(context.Background(), []stats.Run{{StatsRun: host.StatsRun{ID: 1}}})
	if err == nil {
		t.Fatal("a 400 from the store was not an error")
	}
	if got := err.Error(); !contains(got, "each run needs run_id") {
		t.Errorf("the error lost the store's own words: %q", got)
	}
}

// An unconfigured client is nil rather than broken, so the command can
// refuse before it spends the host's rate limit on a pass with nowhere
// to write.
func TestAnUnconfiguredStoreIsNil(t *testing.T) {
	for _, tc := range [][3]string{
		{"", "p", "t"},
		{"https://x", "", "t"},
		{"https://x", "p", ""},
	} {
		if w := NewWorker(tc[0], tc[1], tc[2]); w != nil {
			t.Errorf("NewWorker(%q, %q, %q) built a client", tc[0], tc[1], tc[2])
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
