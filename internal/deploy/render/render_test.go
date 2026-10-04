package render

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveRaw writes body verbatim, so a test can assert what the decoder
// does with a shape the adapter is not supposed to accept.
func serveRaw(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer rnd_test" {
			t.Errorf("Authorization = %q", got)
		}
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
}

func serve(t *testing.T, payload any) *httptest.Server {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return serveRaw(t, http.StatusOK, string(b))
}

// dep is one element of the list: the deploy wrapped in its cursor
// envelope, which is the shape Render returns.
func dep(status, sha, createdAt string) map[string]any {
	return map[string]any{
		"cursor": "c_" + sha,
		"deploy": map[string]any{
			"status":    status,
			"commit":    map[string]any{"id": sha},
			"createdAt": createdAt,
		},
	}
}

func TestStateLive(t *testing.T) {
	srv := serve(t, []any{dep("live", "sha_a", "2026-09-16T10:00:00Z")})
	defer srv.Close()
	s, err := New(srv.URL, "rnd_test").State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "sha_a" || s.Failed {
		t.Errorf("state = %+v", s)
	}
}

// Render's failure half is three statuses plus canceled, and an unmapped
// one reads as in-flight — a Blocked ticket riding to the deploy timeout
// instead. So each is named rather than one standing for the rest.
func TestStateEveryFailureStatusWithOlderLive(t *testing.T) {
	for _, status := range []string{"build_failed", "update_failed", "pre_deploy_failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			srv := serve(t, []any{
				dep(status, "sha_new", "2026-09-16T11:00:00Z"),
				dep("live", "sha_old", "2026-09-16T10:00:00Z"),
			})
			defer srv.Close()
			s, err := New(srv.URL, "rnd_test").State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			// A failed deploy on Render leaves the previous instance
			// serving, so both halves are true and name different commits.
			if s.ActiveSHA != "sha_old" || !s.Failed || s.FailedSHA != "sha_new" {
				t.Errorf("state = %+v", s)
			}
		})
	}
}

func TestStateInFlightIsNeither(t *testing.T) {
	for _, status := range []string{"created", "queued", "build_in_progress", "pre_deploy_in_progress", "update_in_progress"} {
		t.Run(status, func(t *testing.T) {
			srv := serve(t, []any{
				dep(status, "sha_new", "2026-09-16T11:00:00Z"),
				dep("live", "sha_old", "2026-09-16T10:00:00Z"),
			})
			defer srv.Close()
			s, err := New(srv.URL, "rnd_test").State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if s.ActiveSHA != "sha_old" || s.Failed {
				t.Errorf("state = %+v", s)
			}
		})
	}
}

// A superseded deploy is neither serving nor failed. Mapping it to failed
// would Block every ticket whose merge has since been deployed over.
func TestStateDeactivatedIsNeither(t *testing.T) {
	srv := serve(t, []any{
		dep("deactivated", "sha_old", "2026-09-16T10:00:00Z"),
	})
	defer srv.Close()
	s, err := New(srv.URL, "rnd_test").State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "" || s.Failed {
		t.Errorf("state = %+v", s)
	}
}

// The adapter sorts rather than trusting the API's order, so the newest
// deploy decides Failed even when the response lists it last. Given in
// response order this payload says the live deploy is newest and nothing
// failed; by createdAt it says the opposite.
func TestStateOrdersByCreatedAtNotByResponseOrder(t *testing.T) {
	srv := serve(t, []any{
		dep("live", "sha_old", "2026-09-16T10:00:00Z"),
		dep("build_failed", "sha_new", "2026-09-16T11:00:00Z"),
	})
	defer srv.Close()
	s, err := New(srv.URL, "rnd_test").State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "sha_old" || !s.Failed || s.FailedSHA != "sha_new" {
		t.Errorf("state = %+v", s)
	}
}

// The envelope is a bare array. The failure this guards is the quiet one:
// an object-shaped payload decoded into a list yields zero deploys and a
// successful call, which the plane cannot tell from a service that has
// never deployed. Assert it errors instead.
func TestStateRejectsAnObjectEnvelope(t *testing.T) {
	srv := serveRaw(t, http.StatusOK, `{"deployments":[{"status":"live","commit":{"id":"sha_a"}}]}`)
	defer srv.Close()
	s, err := New(srv.URL, "rnd_test").State(context.Background())
	if err == nil {
		t.Fatalf("object envelope decoded without error, state = %+v", s)
	}
	if !strings.Contains(err.Error(), "render: decoding deploys") {
		t.Errorf("err = %v", err)
	}
}

func TestStateNonOKIsAnError(t *testing.T) {
	srv := serveRaw(t, http.StatusUnauthorized, `{"message":"unauthorized"}`)
	defer srv.Close()
	if _, err := New(srv.URL, "rnd_test").State(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("err = %v", err)
	}
}

// Nothing deployed yet: not an error, and not a failure.
func TestStateEmptyList(t *testing.T) {
	srv := serve(t, []any{})
	defer srv.Close()
	s, err := New(srv.URL, "rnd_test").State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "" || s.Failed {
		t.Errorf("state = %+v", s)
	}
}

// If Render ever stops sending createdAt — renames it, or omits it on
// some deploy kind — every record parses to the zero time, they all
// compare equal, and the stable sort degrades to the response's own
// order rather than to an arbitrary one. That is the whole reason the
// fallback is the zero time and the sort is stable.
func TestStateWithNoTimestampsFallsBackToResponseOrder(t *testing.T) {
	srv := serve(t, []any{
		dep("build_failed", "sha_new", ""),
		dep("live", "sha_old", ""),
	})
	defer srv.Close()
	s, err := New(srv.URL, "rnd_test").State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "sha_old" || !s.Failed || s.FailedSHA != "sha_new" {
		t.Errorf("state = %+v", s)
	}
}

// One unparseable timestamp among good ones sorts last, so a malformed
// record cannot become the newest deploy and decide Failed on behalf of
// deploys that are genuinely newer.
func TestStateUnparseableTimestampDoesNotDecideFailed(t *testing.T) {
	srv := serve(t, []any{
		dep("build_failed", "sha_bad", "not-a-timestamp"),
		dep("live", "sha_live", "2026-09-16T10:00:00Z"),
	})
	defer srv.Close()
	s, err := New(srv.URL, "rnd_test").State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "sha_live" || s.Failed {
		t.Errorf("state = %+v", s)
	}
}
