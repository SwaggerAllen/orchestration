package ghdeploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, deployments []map[string]any, statuses map[string][]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gh_test" {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/deployments"):
			if got := r.URL.Query().Get("environment"); got != "production" {
				t.Errorf("environment = %q", got)
			}
			_ = json.NewEncoder(w).Encode(deployments)
		case strings.Contains(r.URL.Path, "/statuses"):
			parts := strings.Split(r.URL.Path, "/")
			id := parts[len(parts)-2]
			_ = json.NewEncoder(w).Encode(statuses[id])
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
}

func TestStateReadsNewestFirst(t *testing.T) {
	srv := serve(t,
		[]map[string]any{
			{"id": 2, "sha": "sha_new"},
			{"id": 1, "sha": "sha_old"},
		},
		map[string][]map[string]any{
			"2": {{"state": "failure"}},
			"1": {{"state": "success"}},
		})
	defer srv.Close()

	c, err := New("swaggerallen/dummy", "gh_test", "production", WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.Failed || s.FailedSHA != "sha_new" || s.ActiveSHA != "sha_old" {
		t.Errorf("state = %+v", s)
	}
}

func TestStateSuccess(t *testing.T) {
	srv := serve(t,
		[]map[string]any{{"id": 3, "sha": "sha_c"}},
		map[string][]map[string]any{"3": {{"state": "success"}}})
	defer srv.Close()

	c, err := New("swaggerallen/dummy", "gh_test", "production", WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSHA != "sha_c" || s.Failed {
		t.Errorf("state = %+v", s)
	}
}
