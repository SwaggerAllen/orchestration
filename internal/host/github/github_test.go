package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/host"
)

func fakeGitHub(t *testing.T, handle func(r *http.Request, body map[string]any) (int, any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gh_test" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		status, resp := handle(r, body)
		w.WriteHeader(status)
		if resp != nil {
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Errorf("encoding response: %v", err)
			}
		}
	}))
}

func client(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New("swaggerallen/dummy", "gh_test", WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDispatchWorkflow(t *testing.T) {
	srv := fakeGitHub(t, func(r *http.Request, body map[string]any) (int, any) {
		if r.URL.Path != "/repos/swaggerallen/dummy/actions/workflows/pipeline-agent-dev.yml/dispatches" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if body["ref"] != "main" {
			t.Errorf("ref = %v", body["ref"])
		}
		inputs := body["inputs"].(map[string]any)
		if inputs["ticket"] != "PIPE-12" {
			t.Errorf("inputs = %v", inputs)
		}
		return http.StatusNoContent, nil
	})
	defer srv.Close()
	if err := client(t, srv).DispatchWorkflow(context.Background(), "pipeline-agent-dev.yml", map[string]string{"ticket": "PIPE-12"}); err != nil {
		t.Fatal(err)
	}
}

func TestListAgentRunsParsesConvention(t *testing.T) {
	srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"workflow_runs": []map[string]any{
			{"id": 1, "display_title": "pipeline: dev PIPE-12", "status": "in_progress", "updated_at": "2026-01-01T01:00:00Z", "html_url": "https://gh/1"},
			{"id": 2, "display_title": "pipeline: boundary PIPE-30", "status": "completed", "updated_at": "2026-01-01T02:00:00Z", "html_url": "https://gh/2"},
			{"id": 3, "display_title": "ci", "status": "completed", "updated_at": "2026-01-01T03:00:00Z", "html_url": "https://gh/3"},
			{"id": 4, "display_title": "pipeline: sweep", "status": "completed", "updated_at": "2026-01-01T04:00:00Z", "html_url": "https://gh/4"},
		}}
	})
	defer srv.Close()
	runs, err := client(t, srv).ListAgentRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want the 2 agent runs only", runs)
	}
	if runs[0].Kind != "dev" || runs[0].TicketKey != "PIPE-12" || !runs[0].Live {
		t.Errorf("run 0 = %+v", runs[0])
	}
	if runs[1].Kind != "boundary" || runs[1].Live {
		t.Errorf("run 1 = %+v", runs[1])
	}
}

func TestChecksForAggregates(t *testing.T) {
	cases := []struct {
		name string
		runs []map[string]any
		want host.CheckStatus
	}{
		{"green", []map[string]any{
			{"status": "completed", "conclusion": "success"},
			{"status": "completed", "conclusion": "skipped"},
		}, host.ChecksGreen},
		{"red beats pending", []map[string]any{
			{"status": "in_progress", "conclusion": ""},
			{"status": "completed", "conclusion": "failure", "html_url": "https://gh/fail"},
		}, host.ChecksRed},
		{"pending", []map[string]any{
			{"status": "completed", "conclusion": "success"},
			{"status": "queued", "conclusion": ""},
		}, host.ChecksPending},
		{"none", nil, host.ChecksNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
				if !strings.Contains(r.URL.Path, "/commits/sha1/check-runs") {
					t.Errorf("path = %s", r.URL.Path)
				}
				return http.StatusOK, map[string]any{"check_runs": tc.runs}
			})
			defer srv.Close()
			got, err := client(t, srv).ChecksFor(context.Background(), "sha1")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q", got.Status, tc.want)
			}
			if tc.want == host.ChecksRed && got.RunURL == "" {
				t.Error("red verdict must carry the failing run URL")
			}
		})
	}
}

func TestCreatePRAndMarkReady(t *testing.T) {
	var graphqlCalled bool
	srv := fakeGitHub(t, func(r *http.Request, body map[string]any) (int, any) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/swaggerallen/dummy/pulls":
			if body["head"] != "pipe-12-cap" || body["base"] != "main" || body["draft"] != false {
				t.Errorf("create body = %v", body)
			}
			return http.StatusCreated, map[string]any{
				"number": 7, "html_url": "https://gh/pr/7",
				"head": map[string]any{"ref": "pipe-12-cap", "sha": "sha7"},
			}
		case r.Method == http.MethodGet && r.URL.Path == "/repos/swaggerallen/dummy/pulls/7":
			return http.StatusOK, map[string]any{"node_id": "node_7", "draft": true}
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			graphqlCalled = true
			if !strings.Contains(body["query"].(string), "markPullRequestReadyForReview") {
				t.Errorf("graphql query = %v", body["query"])
			}
			if body["variables"].(map[string]any)["id"] != "node_7" {
				t.Errorf("graphql vars = %v", body["variables"])
			}
			return http.StatusOK, map[string]any{"data": map[string]any{}}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			return http.StatusNotFound, nil
		}
	})
	defer srv.Close()

	c := client(t, srv)
	pr, err := c.CreatePR(context.Background(), "pipe-12-cap", "PIPE-12 Cap", "body", false)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 || pr.HeadSHA != "sha7" {
		t.Errorf("pr = %+v", pr)
	}
	if err := c.MarkPRReady(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if !graphqlCalled {
		t.Error("undraft must go through GraphQL — REST cannot flip the draft flag")
	}
}

// A 403 here is nearly always the calling workflow's permissions block,
// not the token — and the bare status sends you to the wrong place.
func TestForbiddenExplainsWorkflowPermissions(t *testing.T) {
	srv := fakeGitHub(t, func(*http.Request, map[string]any) (int, any) {
		return http.StatusForbidden, map[string]any{"message": "Resource not accessible by integration"}
	})
	defer srv.Close()

	_, err := client(t, srv).ListOpenPRs(context.Background())
	if err == nil || !strings.Contains(err.Error(), "permissions:") {
		t.Errorf("a 403 must point at the workflow's permissions block, got %v", err)
	}
}
