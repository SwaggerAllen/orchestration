package state

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

func TestWorkerRoundTripsAMove(t *testing.T) {
	var got wireMove
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("authorization")
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]wireMove{
			"tkt_1": {From: "design_review", To: "ready_for_dev", Role: "author"},
		})
	}))
	defer srv.Close()

	s := NewWorker(srv.URL, "catapult", "sec")
	if err := s.Record(context.Background(), "tkt_1", core.RecordedMove{
		From: protocol.Designing, To: protocol.DesignReview, Role: core.RoleDesign,
	}); err != nil {
		t.Fatal(err)
	}
	if path != "/state/catapult/record" {
		t.Errorf("path = %q — the project segment is what keeps two projects' rows apart", path)
	}
	if auth != "Bearer sec" {
		t.Errorf("authorization = %q", auth)
	}
	if got.From != "designing" || got.To != "design_review" || got.Role != "design" {
		t.Errorf("recorded %+v, want the full edge and the role", got)
	}

	all, err := s.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rec := all["tkt_1"]
	if rec.From != protocol.DesignReview || rec.To != protocol.ReadyForDev || rec.Role != core.RoleAuthor {
		t.Errorf("read back %+v", rec)
	}
}

// The caller declines to transition when Record fails, so a non-200 has
// to arrive as an error rather than as a silent success.
func TestWorkerReportsAFailedWrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := NewWorker(srv.URL, "catapult", "wrong").Record(context.Background(), "tkt_1", core.RecordedMove{})
	if err == nil {
		t.Fatal("a rejected write was reported as success")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the error hides the status: %v", err)
	}
}

// And a failed read must not look like an empty store, since empty means
// "judge nothing" and would turn every invariant off.
func TestWorkerReportsAFailedRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	all, err := NewWorker(srv.URL, "catapult", "sec").All(context.Background())
	if err == nil {
		t.Fatal("a failed read returned a map")
	}
	if all != nil {
		t.Error("a failed read returned a map as well as an error")
	}
}

// An unconfigured project gets no client at all rather than one that
// errors on every call: no store means no records means nothing judged,
// which is where a project that has not been wired up belongs.
func TestAnUnconfiguredProjectGetsNoClient(t *testing.T) {
	for _, c := range []struct{ base, project, token string }{
		{"", "p", "t"}, {"https://x", "", "t"}, {"https://x", "p", ""},
	} {
		if got := NewWorker(c.base, c.project, c.token); got != nil {
			t.Errorf("NewWorker(%q,%q,%q) built a client", c.base, c.project, c.token)
		}
	}
}

// The Store interface is what the plane depends on; both implementations
// have to satisfy it or the wiring fails at run time rather than here.
func TestBothImplementationsSatisfyTheStore(t *testing.T) {
	var _ Store = NewMemory()
	var _ Store = &Worker{}
}

func TestMemoryReportsInjectedFailures(t *testing.T) {
	m := NewMemory()
	m.FailWrites = errors.New("down")
	if err := m.Record(context.Background(), "t", core.RecordedMove{}); err == nil {
		t.Error("FailWrites did not fail")
	}
	m.FailWrites, m.FailReads = nil, errors.New("down")
	if _, err := m.All(context.Background()); err == nil {
		t.Error("FailReads did not fail")
	}
}

// NewWorker returns a typed nil for an unconfigured project, so the
// caller must convert it to a nil *interface* rather than assigning it
// straight into one. This is the shape of that bug, pinned: if the
// helper ever returns the pointer directly into a Store, this fails.
func TestATypedNilIsNotAUsableStore(t *testing.T) {
	var s Store
	if w := NewWorker("", "", ""); w != nil {
		s = w
	}
	if s != nil {
		t.Fatal("an unconfigured project produced a non-nil Store")
	}
}

// Two sweeps racing both call Reserve and exactly one is told it won.
// The loser gets the ticket already holding it, which is what turns a
// duplicate dispatch into no dispatch at all.
func TestReserveReportsTheHolderToTheLoser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/reserve") {
			_ = json.NewEncoder(w).Encode(map[string]any{"granted": false, "heldBy": "tkt_1"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	held, err := NewWorker(srv.URL, "p", "sec").Reserve(context.Background(), core.AgentBoundary, "tkt_2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if held != "tkt_1" {
		t.Errorf("heldBy = %q, want the ticket already holding it", held)
	}
}

// A refusal that names nobody is still a refusal. Returning "" there
// would read as granted and dispatch the second agent — the exact
// failure the reservation exists to prevent.
func TestARefusalWithNoNamedHolderIsStillARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"granted": false})
	}))
	defer srv.Close()

	held, err := NewWorker(srv.URL, "p", "sec").Reserve(context.Background(), core.AgentDev, "tkt_2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if held == "" {
		t.Error("an unnamed refusal read as a grant")
	}
}

func TestMemoryReservesOncePerKind(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	if held, _ := m.Reserve(ctx, core.AgentBoundary, "t1", time.Minute); held != "" {
		t.Fatalf("first reservation refused, held by %q", held)
	}
	if held, _ := m.Reserve(ctx, core.AgentBoundary, "t2", time.Minute); held != "t1" {
		t.Errorf("second reservation granted or misreported: %q", held)
	}
	// The holder re-reserving is a resume, not a second agent.
	if held, _ := m.Reserve(ctx, core.AgentBoundary, "t1", time.Minute); held != "" {
		t.Errorf("the holder was refused its own reservation: %q", held)
	}
	// A different kind is a different agent.
	if held, _ := m.Reserve(ctx, core.AgentDev, "t2", time.Minute); held != "" {
		t.Errorf("one kind's reservation blocked another: %q", held)
	}
	if err := m.Release(ctx, core.AgentBoundary, "t1"); err != nil {
		t.Fatal(err)
	}
	if held, _ := m.Reserve(ctx, core.AgentBoundary, "t2", time.Minute); held != "" {
		t.Errorf("release did not free the kind: %q", held)
	}
}
