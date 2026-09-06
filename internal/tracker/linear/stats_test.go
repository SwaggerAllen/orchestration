package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serve answers with the given pages in order and records every request
// body, so a test can assert what the query actually asked for.
func serve(t *testing.T, pages ...string) (*Client, *[]string) {
	t.Helper()
	var bodies []string
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		i := n
		if i >= len(pages) {
			i = len(pages) - 1
		}
		n++
		_, _ = io.WriteString(w, pages[i])
	}))
	t.Cleanup(srv.Close)
	return New("lin_api_test", WithEndpoint(srv.URL)), &bodies
}

// THE assertion this file exists for. Archived issues are the oldest
// ones, so a query that omits them under-counts the earliest milestones
// and renders as a downward trend with nothing about it looking wrong.
// Pinned on the wire rather than on behaviour, because a fake tracker
// cannot tell us what Linear's default filter does.
func TestTheStatsQueryAsksForArchivedIssues(t *testing.T) {
	c, bodies := serve(t, `{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`)
	if _, err := c.ListIssuesForStats(context.Background(), "team", "proj"); err != nil {
		t.Fatal(err)
	}
	if len(*bodies) != 1 {
		t.Fatalf("made %d requests, want 1", len(*bodies))
	}
	if !strings.Contains((*bodies)[0], "includeArchived: true") {
		t.Errorf("the stats query does not ask for archived issues:\n%s", (*bodies)[0])
	}
	// The terminal timestamps, the state name, and its TYPE. The type
	// is what the default aggregate excludes terminal states by —
	// Linear's built-in Duplicate has no protocol slug, so a list of
	// names would silently start counting it.
	for _, want := range []string{
		"archivedAt", "completedAt", "canceledAt",
		"state { name type }", "fromState { name type }", "toState { name type }",
	} {
		if !strings.Contains((*bodies)[0], want) {
			t.Errorf("the stats query does not select %q", want)
		}
	}
}

const onePage = `{"data":{"issues":{"nodes":[
  {"identifier":"ORC-23","title":"Add a farewell","priority":3,
   "createdAt":"2026-08-13T01:00:00Z","completedAt":"2026-08-13T05:00:00Z",
   "canceledAt":null,"archivedAt":"2026-08-14T00:19:06Z",
   "state":{"name":"Done","type":"completed"},"projectMilestone":{"name":"Hookup"},
   "labels":{"nodes":[{"name":"bug"}],"pageInfo":{"hasNextPage":false}},
   "history":{"nodes":[
     {"createdAt":"2026-08-13T05:00:00Z","fromState":{"name":"In Progress","type":"started"},"toState":{"name":"Done","type":"completed"}},
     {"createdAt":"2026-08-13T02:00:00Z","fromState":{"name":"Todo","type":"unstarted"},"toState":{"name":"In Progress","type":"started"}},
     {"createdAt":"2026-08-13T01:30:00Z","fromState":null,"toState":null}
   ],"pageInfo":{"hasNextPage":false}}}
],"pageInfo":{"hasNextPage":false}}}}`

func TestAnArchivedIssueComesBackWithItsHistory(t *testing.T) {
	c, _ := serve(t, onePage)
	got, err := c.ListIssuesForStats(context.Background(), "team", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1", len(got))
	}
	i := got[0]
	if i.Key != "ORC-23" || i.Milestone != "Hookup" || i.CurrentState != "Done" {
		t.Errorf("issue = %+v", i)
	}
	if !i.Archived() {
		t.Error("archivedAt did not reach the issue")
	}
	if want := time.Date(2026, 8, 13, 5, 0, 0, 0, time.UTC); !i.CompletedAt.Equal(want) {
		t.Errorf("completedAt = %v, want %v", i.CompletedAt, want)
	}
	if !i.CanceledAt.IsZero() {
		t.Errorf("a null canceledAt became %v", i.CanceledAt)
	}
	if len(i.Labels) != 1 || i.Labels[0] != "bug" {
		t.Errorf("labels = %v", i.Labels)
	}
}

// Linear's connections come back newest first and stats.Issue promises
// oldest first. A reversed history is not a visible failure: it inverts
// every interval, so each state's span becomes the one that preceded it,
// and the numbers stay entirely plausible.
func TestHistoryComesBackOldestFirst(t *testing.T) {
	c, _ := serve(t, onePage)
	got, _ := c.ListIssuesForStats(context.Background(), "team", "proj")
	h := got[0].History
	if len(h) != 2 {
		t.Fatalf("got %d transitions, want 2 (the non-state entry must be dropped)", len(h))
	}
	if h[0].To != "In Progress" || h[1].To != "Done" {
		t.Fatalf("history is %v -> %v, want oldest first", h[0].To, h[1].To)
	}
	for n := 1; n < len(h); n++ {
		if h[n].At.Before(h[n-1].At) {
			t.Errorf("transition %d is older than the one before it", n)
		}
	}
}

// A history entry that is not a state change — a label edit, an assignee
// change — carries no toState and must not become an interval.
func TestANonStateHistoryEntryIsNotATransition(t *testing.T) {
	c, _ := serve(t, onePage)
	got, _ := c.ListIssuesForStats(context.Background(), "team", "proj")
	for _, tr := range got[0].History {
		if tr.To == "" {
			t.Error("a history entry with no toState became a transition")
		}
	}
}

func TestStatsIssuesPaginate(t *testing.T) {
	first := `{"data":{"issues":{"nodes":[
	  {"identifier":"ORC-1","createdAt":"2026-08-12T00:00:00Z","state":{"name":"Todo"},
	   "labels":{"nodes":[],"pageInfo":{"hasNextPage":false}},
	   "history":{"nodes":[],"pageInfo":{"hasNextPage":false}}}
	],"pageInfo":{"hasNextPage":true,"endCursor":"cur1"}}}}`
	second := `{"data":{"issues":{"nodes":[
	  {"identifier":"ORC-2","createdAt":"2026-08-13T00:00:00Z","state":{"name":"Done","type":"completed"},
	   "labels":{"nodes":[],"pageInfo":{"hasNextPage":false}},
	   "history":{"nodes":[],"pageInfo":{"hasNextPage":false}}}
	],"pageInfo":{"hasNextPage":false}}}}`
	c, bodies := serve(t, first, second)
	got, err := c.ListIssuesForStats(context.Background(), "team", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d issues across two pages, want 2", len(got))
	}
	var v struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte((*bodies)[1]), &v); err != nil {
		t.Fatal(err)
	}
	if v.Variables["after"] != "cur1" {
		t.Errorf("the second page asked after %v, want cur1", v.Variables["after"])
	}
}

// A truncated history is a ticket whose time in a state is simply wrong,
// with nothing in the output saying so. Refused rather than trimmed —
// the same stance ListIssues takes for escalation counting.
func TestAnOverflowingHistoryIsRefusedRatherThanTruncated(t *testing.T) {
	overflow := `{"data":{"issues":{"nodes":[
	  {"identifier":"ORC-9","createdAt":"2026-08-12T00:00:00Z","state":{"name":"Todo"},
	   "labels":{"nodes":[],"pageInfo":{"hasNextPage":false}},
	   "history":{"nodes":[],"pageInfo":{"hasNextPage":true}}}
	],"pageInfo":{"hasNextPage":false}}}}`
	c, _ := serve(t, overflow)
	_, err := c.ListIssuesForStats(context.Background(), "team", "proj")
	if err == nil {
		t.Fatal("a truncated history was accepted")
	}
	if !strings.Contains(err.Error(), "ORC-9") {
		t.Errorf("the error does not name the issue: %v", err)
	}
}

// The category rides with the state, and the store excludes terminal
// states by it rather than by name.
func TestTheStateCategoryComesBackWithTheState(t *testing.T) {
	c, _ := serve(t, onePage)
	got, err := c.ListIssuesForStats(context.Background(), "team", "proj")
	if err != nil {
		t.Fatal(err)
	}
	i := got[0]
	if i.CurrentCategory != "completed" {
		t.Errorf("current category = %q, want completed", i.CurrentCategory)
	}
	if i.History[1].Category != "completed" {
		t.Errorf("the Done transition's category = %q, want completed", i.History[1].Category)
	}
	if i.History[0].FromCategory != "unstarted" {
		t.Errorf("the created-in state's category = %q, want unstarted", i.History[0].FromCategory)
	}
}
