// Package statsstore is the client for the ProjectStats Durable Object
// — the pipeline's measurement of itself (DESIGN §13).
//
// A sibling of internal/state rather than part of it, mirroring the
// split on the Worker side: two Durable Object classes in one Worker,
// because Durable Objects serialize requests to one instance and
// ProjectState is on the dispatch path. A dashboard aggregation queued
// ahead of a reservation would widen exactly the window the reservation
// exists to close.
//
// It is also a sibling of internal/stats rather than a file inside it.
// That package states that it does no I/O, and it means it: the
// derivation is pure so a defect in it is repaired by running the
// collector again. This is where the pure result meets the wire.
//
// THE WIRE IS IN MILLISECONDS. Every timestamp the store keeps is epoch
// milliseconds, because the read side buckets with
// `strftime(fmt, col / 1000, 'unixepoch')`. Sending seconds does not
// fail — it lands every row in 1970, which renders as an empty chart
// with a stray leftmost bucket. The conversion happens here, in one
// place, with a test that pins the unit.
package statsstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/stats"
)

// Worker is the ProjectStats client. Same origin, same shared secret
// and same per-project path segment as the move record's client — the
// object behind /stats/<project>/ is a different class, not a different
// deployment.
type Worker struct {
	BaseURL string
	Project string
	Token   string
	HTTP    *http.Client
}

// NewWorker builds the client, or nil when it has not been configured.
//
// Nil rather than a broken client, matching state.NewWorker. The
// difference is what the caller does with it: an unconfigured move
// record means the invariants go quiet, which is why that one warns and
// carries on. An unconfigured stats store means the collector has
// nowhere to put what it spent GitHub's rate limit collecting, so the
// command refuses instead.
func NewWorker(baseURL, project, token string) *Worker {
	if baseURL == "" || project == "" || token == "" {
		return nil
	}
	return &Worker{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Project: project,
		Token:   token,
		// Longer than the move record's ten seconds, and for the
		// opposite reason. That one is on the dispatch path, where
		// failing fast and retrying next beat beats waiting. This runs
		// once a night and writes hundreds of rows in a request; a
		// timeout here costs the whole pass, and the pass costs rate
		// limit that does not come back for an hour.
		HTTP: &http.Client{Timeout: 60 * time.Second},
	}
}

// ---- wire types ---------------------------------------------------
//
// Separate from the stats package's own types on purpose. Those hold
// time.Time, which is the right shape to reason about; these hold epoch
// milliseconds, which is what the store keeps. Tagging the domain types
// directly would put the unit conversion in a MarshalJSON nobody reads.

type wireInterval struct {
	State     string `json:"state"`
	Category  string `json:"category"`
	EnteredAt int64  `json:"entered_at"`
	LeftAt    *int64 `json:"left_at"`
}

type wireTicket struct {
	Key         string         `json:"key"`
	Title       string         `json:"title"`
	CreatedAt   int64          `json:"created_at"`
	CompletedAt *int64         `json:"completed_at"`
	CanceledAt  *int64         `json:"canceled_at"`
	Priority    int            `json:"priority"`
	IsBoundary  bool           `json:"is_boundary"`
	Archived    bool           `json:"archived"`
	Labels      []string       `json:"labels"`
	Intervals   []wireInterval `json:"intervals"`
}

type wireMilestone struct {
	Name              string `json:"name"`
	Kind              string `json:"kind"`
	WindowStart       int64  `json:"window_start"`
	WindowEnd         *int64 `json:"window_end"`
	BoundaryTicket    string `json:"boundary_ticket"`
	BoundaryOpenStart *int64 `json:"boundary_open_start"`
	BoundaryOpenEnd   *int64 `json:"boundary_open_end"`
}

type wireRun struct {
	RunID      int64   `json:"run_id"`
	Repo       string  `json:"repo"`
	Workflow   string  `json:"workflow"`
	RunName    string  `json:"run_name"`
	Kind       string  `json:"kind"`
	TicketKey  *string `json:"ticket_key"`
	StartedAt  int64   `json:"started_at"`
	DurationMS int64   `json:"duration_ms"`
	BillableMS int64   `json:"billable_ms"`
	JobCount   int     `json:"job_count"`
	Conclusion string  `json:"conclusion"`
	Attempt    int     `json:"attempt"`
	IsStatsJob bool    `json:"is_stats_job"`
}

// ms converts a timestamp to what the store keeps. A zero time is
// nil rather than 0: the columns it lands in are nullable precisely
// because "has not happened" and "happened at the epoch" are different
// facts, and 0 would sort as the oldest row in the table.
func ms(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	v := t.UnixMilli()
	return &v
}

// msRequired is the same for a column the store declares NOT NULL.
func msRequired(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- writes -------------------------------------------------------

// PutTickets replaces each ticket's row, labels and intervals.
func (w *Worker) PutTickets(ctx context.Context, tickets []stats.Ticket) error {
	body := make([]wireTicket, 0, len(tickets))
	for _, t := range tickets {
		wt := wireTicket{
			Key: t.Key, Title: t.Title,
			CreatedAt: msRequired(t.CreatedAt), CompletedAt: ms(t.CompletedAt),
			CanceledAt: ms(t.CanceledAt), Priority: t.Priority,
			IsBoundary: t.IsBoundary, Archived: t.Archived,
			// Non-nil so an empty set marshals as [] rather than null.
			// The store iterates `t.labels ?? []` either way; this keeps
			// the request readable when one is being examined by hand,
			// which is how the ms-versus-seconds class of bug is caught.
			Labels:    append([]string{}, t.Labels...),
			Intervals: make([]wireInterval, 0, len(t.Intervals)),
		}
		for _, iv := range t.Intervals {
			wt.Intervals = append(wt.Intervals, wireInterval{
				State: iv.State, Category: iv.Category,
				EnteredAt: msRequired(iv.EnteredAt), LeftAt: ms(iv.LeftAt),
			})
		}
		body = append(body, wt)
	}
	return w.post(ctx, "/tickets", map[string]any{"tickets": body}, nil)
}

// PutMilestones upserts the derived milestone windows.
func (w *Worker) PutMilestones(ctx context.Context, milestones []stats.Milestone) error {
	body := make([]wireMilestone, 0, len(milestones))
	for _, m := range milestones {
		body = append(body, wireMilestone{
			Name: m.Name, Kind: m.Kind,
			WindowStart: msRequired(m.WindowStart), WindowEnd: ms(m.WindowEnd),
			BoundaryTicket:    m.BoundaryTicket,
			BoundaryOpenStart: ms(m.BoundaryOpenStart), BoundaryOpenEnd: ms(m.BoundaryOpenEnd),
		})
	}
	return w.post(ctx, "/milestones", map[string]any{"milestones": body}, nil)
}

// PutResult is what the store did with a batch of runs. Received and
// Inserted differ by the rows it already held: the run table is
// insert-only, so a re-run of the collector reports every row it sent
// and none inserted, which is the mechanism working rather than a
// failure.
type PutResult struct {
	Received int `json:"received"`
	Inserted int `json:"inserted"`
}

// PutRuns sends collected runs. The store ignores a run_id it already
// holds; see PutResult.
func (w *Worker) PutRuns(ctx context.Context, runs []stats.Run) (PutResult, error) {
	body := make([]wireRun, 0, len(runs))
	for _, r := range runs {
		body = append(body, wireRun{
			RunID: r.ID, Repo: r.Repo, Workflow: r.Workflow, RunName: r.Name,
			Kind: r.Kind, TicketKey: optional(r.TicketKey),
			StartedAt: msRequired(r.StartedAt), DurationMS: r.DurationMS,
			BillableMS: r.BillableMS, JobCount: r.JobCount,
			Conclusion: r.Conclusion, Attempt: r.Attempt, IsStatsJob: r.IsStatsJob,
		})
	}
	var out PutResult
	if err := w.post(ctx, "/runs", map[string]any{"runs": body}, &out); err != nil {
		return PutResult{}, err
	}
	return out, nil
}

// PutWatermark records how far a source has been collected.
func (w *Worker) PutWatermark(ctx context.Context, source string, wm stats.Watermark) error {
	return w.post(ctx, "/watermark", map[string]any{
		"source":          source,
		"oldest_complete": ms(wm.OldestComplete),
		"newest_seen":     ms(wm.NewestSeen),
	}, nil)
}

// ---- reads --------------------------------------------------------

// Watermarks returns every source's mark, keyed by source.
//
// A source with no row is absent rather than zero-valued, and the
// collector reads that absence as "collect everything" — which is the
// first pass. That is the same distinction state.Store.All draws for
// the move record, for the same reason: "I have no record" and "I
// recorded the zero value" are different answers.
func (w *Worker) Watermarks(ctx context.Context) (map[string]stats.Watermark, error) {
	var rows []struct {
		Source         string `json:"source"`
		OldestComplete *int64 `json:"oldest_complete"`
		NewestSeen     *int64 `json:"newest_seen"`
	}
	if err := w.get(ctx, "/watermark", &rows); err != nil {
		return nil, err
	}
	out := make(map[string]stats.Watermark, len(rows))
	for _, r := range rows {
		var wm stats.Watermark
		if r.OldestComplete != nil {
			wm.OldestComplete = time.UnixMilli(*r.OldestComplete).UTC()
		}
		if r.NewestSeen != nil {
			wm.NewestSeen = time.UnixMilli(*r.NewestSeen).UTC()
		}
		out[r.Source] = wm
	}
	return out, nil
}

// MinuteRow is one bucket of the minutes read.
type MinuteRow struct {
	Bucket     string `json:"bucket"`
	Series     string `json:"series"`
	Runs       int    `json:"runs"`
	BillableMS int64  `json:"billable_ms"`
	DurationMS int64  `json:"duration_ms"`
	RoundingMS int64  `json:"rounding_ms"`
}

// Minutes reads the stored minutes over a window.
//
// The collector calls it on its own output so the pass prints a total a
// human can hold against the host's billing page. That comparison is
// the only cross-check the arithmetic has, and it cannot be automated
// from here: account billing is not readable by a repository-scoped
// credential, which is every credential this runs under.
func (w *Worker) Minutes(ctx context.Context, from, to time.Time) ([]MinuteRow, error) {
	var out struct {
		Minutes []MinuteRow `json:"minutes"`
	}
	q := fmt.Sprintf("/minutes?from=%d&to=%d&statsJob=1", from.UnixMilli(), to.UnixMilli())
	if err := w.get(ctx, q, &out); err != nil {
		return nil, err
	}
	return out.Minutes, nil
}

// ---- transport ----------------------------------------------------

func (w *Worker) url(op string) string { return w.BaseURL + "/stats/" + w.Project + op }

func (w *Worker) post(ctx context.Context, op string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url(op), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "Bearer "+w.Token)
	req.Header.Set("content-type", "application/json")
	res, err := w.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("stats store: POST %s: HTTP %d: %s", op, res.StatusCode, snippet(res.Body))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("stats store: decoding the reply to POST %s: %w", op, err)
	}
	return nil
}

func (w *Worker) get(ctx context.Context, op string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url(op), nil)
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "Bearer "+w.Token)
	res, err := w.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("stats store: GET %s: HTTP %d: %s", op, res.StatusCode, snippet(res.Body))
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("stats store: decoding the reply to GET %s: %w", op, err)
	}
	return nil
}

func snippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}
