package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingServer answers each request with whatever reply says, and
// records every request body so a test can check that a retry re-sent the
// document rather than an empty one.
func countingServer(t *testing.T, reply func(n int32, w http.ResponseWriter)) (*Client, *int32, *[]string) {
	t.Helper()
	var n int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		reply(atomic.AddInt32(&n, 1), w)
	}))
	t.Cleanup(srv.Close)
	return New("lin_api_test", WithEndpoint(srv.URL)), &n, &bodies
}

func ok(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
}

// The measured failure: the sweep's first read times out and the whole
// beat is lost. Six sweeps went that way on Catapult between 14:00 and
// 15:20 on 2026-09-05.
func TestAReadIsRetriedAndTheDocumentIsResent(t *testing.T) {
	c, n, bodies := countingServer(t, func(n int32, w http.ResponseWriter) {
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		ok(w)
	})
	if err := c.do(context.Background(), "query Whatever { x }", nil, nil); err != nil {
		t.Fatalf("a read that succeeds on the third attempt still failed: %v", err)
	}
	if *n != 3 {
		t.Errorf("made %d attempts, want 3", *n)
	}
	// Every attempt must carry the whole document. The request body is a
	// reader; a retry that reused the drained one would POST nothing and
	// fail as a syntax error instead.
	for i, b := range *bodies {
		if !strings.Contains(b, "query Whatever") {
			t.Errorf("attempt %d sent %q, not the document", i+1, b)
		}
	}
}

// The deliberate half. A timeout awaiting headers says nothing about
// whether the server processed the request, so a retried mutation may be
// a second comment — and the escalation rules count their own markers,
// so one duplicate parks a ticket in Blocked a failure early.
func TestAMutationIsNeverRetried(t *testing.T) {
	c, n, _ := countingServer(t, func(n int32, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
	})
	err := c.do(context.Background(), "mutation CommentCreate($input: CommentCreateInput!) { x }", nil, nil)
	if err == nil {
		t.Fatal("the mutation reported success against a 502")
	}
	if *n != 1 {
		t.Errorf("the mutation was sent %d times — a write must go out once and once only", *n)
	}
	if strings.Contains(err.Error(), "attempts") {
		t.Errorf("the error claims retries that must not have happened: %v", err)
	}
}

// A 4xx is this client's request, not the server's weather: it fails
// identically however often it is sent, and retrying spends the sweep's
// time to learn nothing.
func TestAClientErrorIsNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest} {
		c, n, _ := countingServer(t, func(n int32, w http.ResponseWriter) {
			w.WriteHeader(code)
		})
		if err := c.do(context.Background(), "query Whatever { x }", nil, nil); err == nil {
			t.Fatalf("HTTP %d reported success", code)
		}
		if *n != 1 {
			t.Errorf("HTTP %d was retried %d times", code, *n)
		}
	}
}

// 429 is the one 4xx that is the server's weather.
func TestRateLimitingIsRetried(t *testing.T) {
	c, n, _ := countingServer(t, func(n int32, w http.ResponseWriter) {
		if n < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		ok(w)
	})
	if err := c.do(context.Background(), "query Whatever { x }", nil, nil); err != nil {
		t.Fatalf("a read rate-limited once still failed: %v", err)
	}
	if *n != 2 {
		t.Errorf("made %d attempts, want 2", *n)
	}
}

// A retried failure must not read as a single one, or the next person to
// look measures the wrong thing.
func TestAGivenUpReadSaysHowManyTimesItTried(t *testing.T) {
	c, _, _ := countingServer(t, func(n int32, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
	})
	err := c.do(context.Background(), "query Whatever { x }", nil, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("the error does not say it retried: %v", err)
	}
}

// The measured failure is a transport error, not a status code — every
// other test here drives one of the latter, and this is the one that
// covers `c.http.Do` returning an error at all.
//
// Written as the real thing rather than as a stub transport: the first
// attempt meets a handler that does not answer, against a client whose
// timeout is shorter than the wait, which is literally
// `context deadline exceeded (Client.Timeout exceeded while awaiting
// headers)` — the string Catapult's six lost sweeps carried.
func TestATimeoutAwaitingHeadersIsRetried(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			// A plain bounded sleep, longer than the client's deadline
			// and shorter than the test. Two other shapes were tried
			// and both wedged: srv.Close waits for outstanding
			// handlers, so a handler released from t.Cleanup deadlocks
			// unless the two cleanups are registered in the LIFO order
			// that puts the release first — and a handler waiting on
			// r.Context().Done() never woke at all. The client's
			// timeout did not reach the server's request context here;
			// this ran 139s and was killed.
			time.Sleep(700 * time.Millisecond)
			return
		}
		ok(w)
	}))
	t.Cleanup(srv.Close)

	// 250ms rather than something tighter: the margin is against a
	// loaded machine answering a localhost round-trip slowly, which
	// would time out the *second* attempt too and fail the count below
	// for no reason.
	c := New("lin_api_test",
		WithEndpoint(srv.URL),
		WithHTTPClient(&http.Client{Timeout: 250 * time.Millisecond}))

	if err := c.do(context.Background(), "query Whatever { x }", nil, nil); err != nil {
		t.Fatalf("a read whose first attempt timed out was not retried to success: %v", err)
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Errorf("server saw %d requests, want 2", got)
	}
}

// The same failure, given up on: a transport error must carry the count
// too, or the reader of a real outage measures one timeout when there
// were three.
func TestAGivenUpTransportFailureSaysHowManyTimesItTried(t *testing.T) {
	// Nothing is listening: Do fails at connect, with no server to
	// count for us.
	c := New("lin_api_test", WithEndpoint("http://127.0.0.1:1"))
	err := c.do(context.Background(), "query Whatever { x }", nil, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("a transport failure did not report its retries: %v", err)
	}
}

// The sweep has a job timeout; burning it asleep between attempts helps
// nobody, so a cancelled context ends the loop rather than waiting out
// the backoff.
func TestACancelledContextStopsRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, n, _ := countingServer(t, func(n int32, w http.ResponseWriter) {
		cancel()
		w.WriteHeader(http.StatusBadGateway)
	})
	start := time.Now()
	err := c.do(ctx, "query Whatever { x }", nil, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	// The request count cannot be the assertion here, and an earlier
	// version of this test made that mistake: once the context is
	// cancelled a further attempt never reaches the server anyway, so
	// the count reads 1 whether the loop stopped or ran to exhaustion.
	// Breaking the ctx.Done branch printed ok against it.
	//
	// These two can tell the difference. The elapsed time is under one
	// backoff interval only if nothing slept, and only the ctx.Done
	// branch words its error this way.
	if d := time.Since(start); d >= readBackoff {
		t.Errorf("slept %s on a cancelled context — the backoff ran anyway", d)
	}
	if !strings.Contains(err.Error(), "gave up after 1 attempt(s)") {
		t.Errorf("the loop did not stop at the cancellation: %v", err)
	}
	if *n != 1 {
		t.Errorf("kept trying after the context was cancelled: %d attempts", *n)
	}
}

// isRead reads the document, not a list of method names beside it, so a
// mutation added later is excluded by being what it is.
func TestIsReadClassifiesTheDocument(t *testing.T) {
	for _, c := range []struct {
		doc  string
		want bool
	}{
		{"query States($teamId: ID!) { x }", true},
		{"  \n  query Whatever { x }", true},
		{"mutation IssueUpdate($id: String!) { x }", false},
		{"  mutation CommentCreate { x }", false},
	} {
		if got := isRead(c.doc); got != c.want {
			t.Errorf("isRead(%q) = %v, want %v", c.doc, got, c.want)
		}
	}
}
