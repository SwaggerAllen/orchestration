package digitalocean

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, payload any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer do_test" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
}

func dep(phase, sha string) map[string]any {
	return map[string]any{
		"phase":    phase,
		"services": []map[string]any{{"source_commit_hash": sha}},
	}
}

func TestStateActive(t *testing.T) {
	srv := serve(t, map[string]any{"deployments": []any{dep("ACTIVE", "sha_a")}})
	defer srv.Close()
	s, err := New(srv.URL, "do_test").State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "sha_a" || s.Failed {
		t.Errorf("state = %+v", s)
	}
}

func TestStateLatestFailedWithOlderActive(t *testing.T) {
	// The newest attempt failed while the older deployment still serves:
	// tickets in the failed build are Blocked, tickets in the active one
	// stay deployed.
	srv := serve(t, map[string]any{"deployments": []any{
		dep("ERROR", "sha_new"),
		dep("ACTIVE", "sha_old"),
	}})
	defer srv.Close()
	s, err := New(srv.URL, "do_test").State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "sha_old" || !s.Failed || s.FailedSHA != "sha_new" {
		t.Errorf("state = %+v", s)
	}
}

func TestStateInFlightIsNeither(t *testing.T) {
	srv := serve(t, map[string]any{"deployments": []any{
		dep("BUILDING", "sha_new"),
		dep("ACTIVE", "sha_old"),
	}})
	defer srv.Close()
	s, err := New(srv.URL, "do_test").State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "sha_old" || s.Failed {
		t.Errorf("state = %+v", s)
	}
}
