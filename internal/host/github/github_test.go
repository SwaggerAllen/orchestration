package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
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

// The conclusion is the whole reason a cancellation is legible at all,
// and it is read from the same listing that already carried it — the
// adapter simply never looked. The JSON below is the shape measured on
// Catapult's own design-agent runs on 2026-08-31: a cancelled run and a
// failed one are both `status: "completed"`, exactly like a successful
// one, and only `conclusion` separates them.
//
// The unmapped case is the point of the last row. GitHub documents
// conclusions this project has never seen (`neutral`, `skipped`,
// `stale`, `timed_out`, `startup_failure`, `action_required`), and a
// mapping that guessed at them would be a claim about the API nobody
// measured. They arrive as OutcomeUnknown, which no rule acts on.
func TestListAgentRunsReadsTheConclusion(t *testing.T) {
	srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"workflow_runs": []map[string]any{
			{"id": 1, "display_title": "pipeline: design PIPE-1", "status": "completed", "conclusion": "cancelled", "updated_at": "2026-01-01T01:00:00Z"},
			{"id": 2, "display_title": "pipeline: design PIPE-2", "status": "completed", "conclusion": "failure", "updated_at": "2026-01-01T01:00:00Z"},
			{"id": 3, "display_title": "pipeline: design PIPE-3", "status": "completed", "conclusion": "success", "updated_at": "2026-01-01T01:00:00Z"},
			{"id": 4, "display_title": "pipeline: design PIPE-4", "status": "in_progress", "conclusion": nil, "updated_at": "2026-01-01T01:00:00Z"},
			{"id": 5, "display_title": "pipeline: design PIPE-5", "status": "completed", "conclusion": "timed_out", "updated_at": "2026-01-01T01:00:00Z"},
		}}
	})
	defer srv.Close()
	runs, err := client(t, srv).ListAgentRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []host.RunOutcome{
		host.OutcomeCancelled, host.OutcomeFailed, host.OutcomeSucceeded,
		host.OutcomeUnknown, host.OutcomeUnknown,
	}
	if len(runs) != len(want) {
		t.Fatalf("runs = %+v, want %d", runs, len(want))
	}
	for i, w := range want {
		if runs[i].Outcome != w {
			t.Errorf("run %d (%s): outcome = %q, want %q", i, runs[i].TicketKey, runs[i].Outcome, w)
		}
	}
	// All three completed rows are equally not-live, which is precisely
	// why `status` alone could not tell a cancellation from a success.
	for i := 0; i < 3; i++ {
		if runs[i].Live {
			t.Errorf("run %d reads as live", i)
		}
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
				if strings.Contains(r.URL.Path, "/jobs") {
					return http.StatusOK, map[string]any{"jobs": []map[string]any{
						{"name": "ci / test", "status": "completed", "conclusion": "failure"},
					}}
				}
				if !strings.Contains(r.URL.Path, "/actions/runs") {
					t.Errorf("path = %s", r.URL.Path)
				}
				return http.StatusOK, map[string]any{"workflow_runs": tc.runs}
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

// The PR-creation 403 has a different cause from every other 403 here,
// and pointing at the permissions block sends you to a block that is
// already correct. It cost a live round: pull-requests: write was
// granted and the call was refused anyway.
func TestForbiddenOnPRCreationNamesTheRepositorySetting(t *testing.T) {
	srv := fakeGitHub(t, func(*http.Request, map[string]any) (int, any) {
		return http.StatusForbidden, map[string]any{"message": "Resource not accessible by integration"}
	})
	defer srv.Close()

	_, err := client(t, srv).CreatePR(context.Background(), "b", "t", "body", true)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "create and approve pull requests") {
		t.Errorf("a PR-creation 403 must name the repository setting, got %v", err)
	}
	// It must still say the permissions block matters — both are needed.
	if !strings.Contains(err.Error(), "pull-requests: write") {
		t.Errorf("and still name the workflow permission, got %v", err)
	}
}

// Reading PRs is refused for the ordinary reason, so it must keep the
// ordinary advice rather than blaming a setting about creation.
func TestForbiddenOnReadingPRsKeepsTheOrdinaryHint(t *testing.T) {
	srv := fakeGitHub(t, func(*http.Request, map[string]any) (int, any) {
		return http.StatusForbidden, map[string]any{"message": "Resource not accessible by integration"}
	})
	defer srv.Close()

	_, err := client(t, srv).ListOpenPRs(context.Background())
	if err == nil || strings.Contains(err.Error(), "create and approve") {
		t.Errorf("a read 403 should point at the permissions block, got %v", err)
	}
}

func TestTailBoundsByLinesAndBytes(t *testing.T) {
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	got := tail(strings.Join(lines, "\n"), 10, 1<<20)
	if n := len(strings.Split(got, "\n")); n != 10 {
		t.Errorf("line bound: got %d lines, want 10", n)
	}
	// The tail, not the head: a runner puts setup at the top and the
	// failure at the bottom, and the failure is the point.
	if !strings.HasSuffix(got, "line 499") {
		t.Errorf("kept the wrong end:\n%s", got)
	}
	// A byte cut lands mid-line; that fragment is dropped rather than
	// presented as a line of output.
	byByte := tail("aaaaaaaaaa\nbbbb\ncccc", 100, 12)
	if strings.Contains(byByte, "a") {
		t.Errorf("a truncated line survived: %q", byByte)
	}
}

// The path this exercises is the one an agent reads at claim time, under
// AGENT_GITHUB_TOKEN. It deliberately touches no check-runs endpoint: a
// fine-grained PAT has no check-runs permission to grant, so a rework run
// that reached for one got a 403 and worked the ticket log-blind.
func TestFailedJobLogsReadsTheActionsAPI(t *testing.T) {
	srv := logServer(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "check-runs") {
			t.Errorf("reached the check-runs API, which the agent token cannot read: %s", r.URL.Path)
		}
		return false
	})
	defer srv.Close()

	got, err := client(t, srv).FailedJobLogs(context.Background(), "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("jobs = %d, want 1 (only the failing job of the failing run)", len(got))
	}
	if got[0].Name != "ci" {
		t.Errorf("name = %q, want ci", got[0].Name)
	}
	if !strings.Contains(got[0].Log, "undefined function farwell/1") {
		t.Errorf("log = %q, want the job's output", got[0].Log)
	}
}

// A job whose log cannot be read is still named — silence would read as
// "it passed" — and the error carries GitHub's own explanation, which is
// the difference between a fixable answer and another debugging round.
func TestFailedJobLogsNamesAJobItCannotRead(t *testing.T) {
	srv := logServer(t, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/logs") {
			return false
		}
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "Resource not accessible by personal access token",
		})
		return true
	})
	defer srv.Close()

	got, err := client(t, srv).FailedJobLogs(context.Background(), "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "ci" {
		t.Fatalf("a job with an unreadable log must still be named: %+v", got)
	}
	if !strings.Contains(got[0].Log, "Resource not accessible by personal access token") {
		t.Errorf("log = %q, want GitHub's own reason for the 403", got[0].Log)
	}
	if !strings.Contains(got[0].Log, "AGENT_GITHUB_TOKEN") {
		t.Errorf("log = %q, want the hint naming which token's permissions apply", got[0].Log)
	}
}

// logServer serves one failing run with one failing job. override runs
// first and reports whether it handled the request itself.
func logServer(t *testing.T, override func(http.ResponseWriter, *http.Request) bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if override != nil && override(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/repos/swaggerallen/dummy/actions/runs":
			if got := r.URL.Query().Get("head_sha"); got != "sha1" {
				t.Errorf("head_sha = %q, want sha1", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{
				{"id": 1, "status": "completed", "conclusion": "success"},
				{"id": 2, "status": "completed", "conclusion": "failure"},
				{"id": 3, "status": "in_progress", "conclusion": ""},
			}})
		case r.URL.Path == "/repos/swaggerallen/dummy/actions/runs/2/jobs":
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{
				{"id": 20, "name": "ci", "status": "completed", "conclusion": "failure",
					"html_url": "https://gh/runs/2/job/20"},
				{"id": 21, "name": "lint", "status": "completed", "conclusion": "success"},
			}})
		case r.URL.Path == "/repos/swaggerallen/dummy/actions/jobs/20/logs":
			_, _ = w.Write([]byte("** (CompileError) undefined function farwell/1"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// Nothing may reach the checks API. Agent runs act as
// AGENT_GITHUB_TOKEN, a fine-grained PAT, and GitHub offers no Checks
// permission to grant one — so that call answers 403 for a reason no
// scope can fix.
//
// This used to allow ChecksFor, on the grounds that only the sweep
// called it and the sweep runs as GITHUB_TOKEN with checks: read. The
// first half was enforced and the second half was never checked, and it
// was false: every agent claim builds a project snapshot, and the
// snapshot reads a CI verdict for every ticket in Checks. Catapult's
// ORC-7 died on a 403 reading ORC-5's check runs, a ticket it had no
// interest in. A carve-out for one caller is only as good as the claim
// about who calls it, so there is no carve-out now.
//
// Written as a scan of every function rather than of one body, so
// moving code between helpers cannot quietly move the call back.
func TestNothingReachesTheChecksAPI(t *testing.T) {
	body, err := os.ReadFile("github.go")
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`^func (?:\(c \*Client\) )?(\w+)`)

	checked := 0
	for _, chunk := range strings.Split(string(body), "\nfunc ") {
		m := name.FindStringSubmatch("func " + chunk)
		if m == nil {
			continue
		}
		var code strings.Builder
		for _, line := range strings.Split(chunk, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue // the rationale for the rule is not a breach of it
			}
			code.WriteString(line + "\n")
		}
		checked++
		for _, bad := range []string{"checkRuns(", "check-runs"} {
			if strings.Contains(code.String(), bad) {
				t.Errorf("%s reaches %q — no fine-grained token can be granted it, and the snapshot runs under one", m[1], bad)
			}
		}
	}
	if checked == 0 {
		t.Fatal("scanned no functions; the split has drifted")
	}
}

// The pipeline's own runs are not the commit's verdict. Agent workflows
// are dispatched and the sweep is scheduled, both against a ref whose
// head can coincide with a ticket branch's — at which point an agent
// run's own status would decide whether that ticket's gates passed.
// check-runs could not confuse the two; the Actions endpoint can, so
// the filter is part of the swap rather than a nicety.
func TestChecksForIgnoresThePipelinesOwnRuns(t *testing.T) {
	srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"workflow_runs": []map[string]any{
			{"status": "completed", "conclusion": "success", "event": "pull_request", "name": "ci"},
			{"status": "in_progress", "conclusion": "", "event": "workflow_dispatch", "name": "pipeline: dev ORC-5"},
			{"status": "completed", "conclusion": "failure", "event": "schedule", "name": "pipeline: sweep",
				"html_url": "https://gh/sweep"},
		}}
	})
	defer srv.Close()

	got, err := client(t, srv).ChecksFor(context.Background(), "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != host.ChecksGreen {
		t.Errorf("status = %q, want %q — a live agent run must not read as pending CI, and a red sweep must not read as a red build", got.Status, host.ChecksGreen)
	}
}

// Red names the failing jobs, not the workflow, because that is the
// grain check-runs gave and the rework scope was written against.
func TestChecksForNamesFailingJobsNotWorkflows(t *testing.T) {
	srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
		if strings.Contains(r.URL.Path, "/jobs") {
			return http.StatusOK, map[string]any{"jobs": []map[string]any{
				{"name": "ci / gates", "status": "completed", "conclusion": "failure"},
				{"name": "ci / audit", "status": "completed", "conclusion": "failure"},
				{"name": "ci / setup", "status": "completed", "conclusion": "success"},
			}}
		}
		return http.StatusOK, map[string]any{"workflow_runs": []map[string]any{
			{"id": 7, "status": "completed", "conclusion": "failure", "event": "pull_request",
				"name": "ci", "html_url": "https://gh/run/7"},
		}}
	})
	defer srv.Close()

	got, err := client(t, srv).ChecksFor(context.Background(), "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != host.ChecksRed || got.RunURL != "https://gh/run/7" {
		t.Fatalf("verdict = %+v, want red linking the failing run", got)
	}
	want := []string{"ci / gates", "ci / audit"}
	if !reflect.DeepEqual(got.FailedJobs, want) {
		t.Errorf("failed jobs = %v, want %v — the passing job must not be named", got.FailedJobs, want)
	}
}

// A jobs read that fails still produces a red verdict, named at
// workflow grain. "ci failed" is worse than "ci / gates failed" and far
// better than red with nothing named, which reads as a build nobody can
// act on.
func TestChecksForKeepsTheVerdictWhenJobsCannotBeRead(t *testing.T) {
	srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
		if strings.Contains(r.URL.Path, "/jobs") {
			return http.StatusForbidden, map[string]any{"message": "nope"}
		}
		return http.StatusOK, map[string]any{"workflow_runs": []map[string]any{
			{"id": 7, "status": "completed", "conclusion": "failure", "event": "pull_request",
				"name": "ci", "html_url": "https://gh/run/7"},
		}}
	})
	defer srv.Close()

	got, err := client(t, srv).ChecksFor(context.Background(), "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != host.ChecksRed {
		t.Fatalf("verdict = %+v, want red", got)
	}
	if !reflect.DeepEqual(got.FailedJobs, []string{"ci"}) {
		t.Errorf("failed jobs = %v, want the workflow name as the fallback", got.FailedJobs)
	}
}

// The window this closes is the reason for the shape: one page of the
// *repository* is one page of whatever it mostly runs, which on a
// pipeline repo is the sweep. Asserting the paths rather than the runs
// is deliberate — the old code returned exactly these runs too, from a
// listing that a busy repository can push them out of.
func TestListAgentRunsReadsOnePagePerWiredAgentWorkflow(t *testing.T) {
	var asked []string
	srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/repos/swaggerallen/dummy/actions/workflows/pipeline-agent-dev.yml/runs":
			return http.StatusOK, map[string]any{"workflow_runs": []map[string]any{
				{"id": 1, "display_title": "pipeline: dev PIPE-12", "status": "in_progress", "updated_at": "2026-01-01T01:00:00Z"},
			}}
		case "/repos/swaggerallen/dummy/actions/workflows/pipeline-agent-boundary.yml/runs":
			return http.StatusOK, map[string]any{"workflow_runs": []map[string]any{
				{"id": 2, "display_title": "pipeline: boundary PIPE-30", "status": "completed", "updated_at": "2026-01-01T02:00:00Z"},
			}}
		}
		return http.StatusOK, map[string]any{"workflow_runs": []map[string]any{}}
	})
	defer srv.Close()

	c, err := New("swaggerallen/dummy", "gh_test", WithBaseURL(srv.URL),
		// The middle entry is an unwired kind, passed through as the
		// config holds it.
		WithAgentWorkflows([]string{"pipeline-agent-dev.yml", "", "pipeline-agent-boundary.yml"}))
	if err != nil {
		t.Fatal(err)
	}
	runs, err := c.ListAgentRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/repos/swaggerallen/dummy/actions/workflows/pipeline-agent-dev.yml/runs",
		"/repos/swaggerallen/dummy/actions/workflows/pipeline-agent-boundary.yml/runs",
	}
	if len(asked) != len(want) {
		t.Fatalf("asked %v, want one listing per wired workflow and nothing else", asked)
	}
	for i, w := range want {
		if asked[i] != w {
			t.Errorf("asked[%d] = %s, want %s", i, asked[i], w)
		}
	}
	if len(runs) != 2 || runs[0].Kind != "dev" || runs[1].Kind != "boundary" {
		t.Fatalf("runs = %+v, want both workflows' runs concatenated", runs)
	}
}

// A kind whose runs cannot be listed reads as a kind that is idle, and an
// idle kind is an invitation to dispatch a second agent — so a workflow
// file the config names and GitHub does not have is fatal, not empty.
func TestListAgentRunsFailsOnAWorkflowFileThatIsNotThere(t *testing.T) {
	srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
		if strings.Contains(r.URL.Path, "pipeline-agent-renamed.yml") {
			return http.StatusNotFound, map[string]any{"message": "Not Found"}
		}
		return http.StatusOK, map[string]any{"workflow_runs": []map[string]any{}}
	})
	defer srv.Close()

	c, err := New("swaggerallen/dummy", "gh_test", WithBaseURL(srv.URL),
		WithAgentWorkflows([]string{"pipeline-agent-renamed.yml"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListAgentRuns(context.Background())
	if err == nil {
		t.Fatal("listing a workflow file that is not there returned no error")
	}
	if !strings.Contains(err.Error(), "pipeline-agent-renamed.yml") {
		t.Errorf("error = %v, want it to name the workflow file the config got wrong", err)
	}
}

// The adapter reads `merged_at`, not `state`. A PR closed without
// merging is `state: "closed"` with `merged_at: null` and no commit
// behind it — reporting one as a merge would put a SHA that never
// landed into the retro note and hand the rehearsal reset a commit to
// revert that does not exist.
func TestMergedPRsForReadsMergedAtNotState(t *testing.T) {
	var gotQuery string
	srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
		gotQuery = r.URL.RawQuery
		// Newest first, which is what the query asks for
		// (sort=updated&direction=desc) and therefore what the real
		// endpoint returns. An ascending fixture would leave the sort
		// below untested — it did, and the probe that removed the sort
		// printed ok.
		return 200, []map[string]any{
			{"number": 10, "merged_at": "2026-08-31T07:37:16Z", "merge_commit_sha": "newer",
				"head": map[string]any{"ref": "orc-199-second-go"}},
			// Closed, never merged: no commit to report.
			{"number": 8, "merged_at": nil, "merge_commit_sha": "",
				"head": map[string]any{"ref": "orc-199-abandoned"}},
			// Another ticket's merge.
			{"number": 9, "merged_at": "2026-08-31T06:00:00Z", "merge_commit_sha": "not-ours",
				"head": map[string]any{"ref": "orc-200-different"}},
			{"number": 7, "merged_at": "2026-08-31T05:29:08Z", "merge_commit_sha": "older",
				"head": map[string]any{"ref": "orc-199-first-go"}},
		}
	})
	defer srv.Close()

	got, err := client(t, srv).MergedPRsFor(t.Context(), "ORC-199")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "state=closed") {
		t.Errorf("query = %q, want the closed PRs — an open one has not merged", gotQuery)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want only ORC-199's two real merges", got)
	}
	if got[0].MergeSHA != "older" || got[1].MergeSHA != "newer" {
		t.Errorf("got %q then %q, want oldest merge first", got[0].MergeSHA, got[1].MergeSHA)
	}
	if got[0].Number != 7 || got[0].Branch != "orc-199-first-go" {
		t.Errorf("first merge = %+v, want PR 7 on its own branch", got[0])
	}
}

// The whole log was already being fetched and thrown away here — the
// tail was taken and the rest dropped in-process. It now travels beside
// the tail so the claim can spill it to a file the agent can search.
func TestFailedJobLogsCarriesTheWholeLogBesideTheTail(t *testing.T) {
	// More lines than the tail keeps, with the failure at the top and
	// cleanup at the bottom — the shape ORC-224 actually had.
	var sb strings.Builder
	sb.WriteString("FAILURE-AT-THE-TOP: test/foo_test.exs:12\n")
	for i := 0; i < maxLogTailLines+50; i++ {
		fmt.Fprintf(&sb, "post-job cleanup line %d\n", i)
	}
	full := sb.String()

	srv := fakeGitHub(t, func(r *http.Request, _ map[string]any) (int, any) {
		switch {
		case strings.Contains(r.URL.Path, "/actions/runs") && strings.Contains(r.URL.RawQuery, "head_sha"):
			return 200, map[string]any{"workflow_runs": []map[string]any{
				{"id": 1, "status": "completed", "conclusion": "failure"}}}
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			return 200, map[string]any{"jobs": []map[string]any{
				{"id": 7, "name": "ci", "status": "completed", "conclusion": "failure",
					"html_url": "https://ci/job/7"}}}
		}
		return 0, nil // the log endpoint is served below
	})
	defer srv.Close()
	c := client(t, srv)
	c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/logs") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(full)),
				Header: make(http.Header)}, nil
		}
		return http.DefaultTransport.RoundTrip(req)
	})}

	jobs, err := c.FailedJobLogs(t.Context(), "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	j := jobs[0]
	if strings.Contains(j.Log, "FAILURE-AT-THE-TOP") {
		t.Fatal("precondition: the failure must fall outside the tail or this asserts nothing")
	}
	if !strings.Contains(j.Full, "FAILURE-AT-THE-TOP") {
		t.Error("the whole log does not carry the failure the tail cut off")
	}
	if want := maxLogTailLines + 51; j.Lines != want {
		t.Errorf("Lines = %d, want %d — the prompt states this to say how much the tail hides", j.Lines, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
