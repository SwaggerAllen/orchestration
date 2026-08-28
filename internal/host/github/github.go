// Package github implements the host port against the GitHub API. The
// control plane runs inside Actions in the project repo, so identity and
// target come from the environment Actions already provides: GITHUB_TOKEN
// and GITHUB_REPOSITORY.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/host"
)

// DefaultBaseURL is the public GitHub API.
const DefaultBaseURL = "https://api.github.com"

// runNameRe matches the stub workflows' run-name convention:
// "pipeline: <kind> <ticket-key>". The convention is the correlation — a
// run named otherwise is not an agent run (PLAN M3).
var runNameRe = regexp.MustCompile(`^pipeline: (design|dev|reconcile|boundary|live-suite) (\S+)`)

// Client implements host.Host.
type Client struct {
	baseURL string
	owner   string
	repo    string
	token   string
	ref     string // branch agent workflow dispatches target
	// agentWorkflows are the workflow files agent runs come from, from
	// the project config's `agents` map. See ListAgentRuns.
	agentWorkflows []string
	http           *http.Client
}

var _ host.Host = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at a test server.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithDispatchRef sets the branch workflow dispatches run on (default main).
func WithDispatchRef(ref string) Option { return func(c *Client) { c.ref = ref } }

// WithAgentWorkflows names the workflow files agent runs come from —
// the project config's `agents` map, whose empty entries ("not wired
// yet") the caller may pass through; they are skipped here. Without it
// ListAgentRuns reads the repository's runs unfiltered, which is the
// window this exists to close.
func WithAgentWorkflows(files []string) Option {
	return func(c *Client) {
		for _, f := range files {
			if f != "" {
				c.agentWorkflows = append(c.agentWorkflows, f)
			}
		}
	}
}

// New builds a client for owner/repo ("swaggerallen/orchestration-dummy").
func New(repository, token string, opts ...Option) (*Client, error) {
	owner, repo, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("github: repository %q is not owner/repo", repository)
	}
	c := &Client{
		baseURL: DefaultBaseURL, owner: owner, repo: repo, token: token, ref: "main",
		http: &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// apiError carries the status code alongside the message, so a caller
// that has a use for a specific one can ask. Only 404 has such a use so
// far — "the file is not there" is an answer rather than a failure —
// and the rendered text is unchanged, because it is read far more often
// than it is inspected.
type apiError struct {
	method, path string
	status       int
	msg, hint    string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("github: %s %s: HTTP %d%s%s", e.method, e.path, e.status, e.msg, e.hint)
}

func isNotFound(err error) bool {
	var e *apiError
	return errors.As(err, &e) && e.status == http.StatusNotFound
}

func (c *Client) rest(ctx context.Context, method, path string, body, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &apiError{
			method: method, path: path, status: resp.StatusCode,
			msg: apiMessage(resp), hint: hint(resp.StatusCode, method, path),
		}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// hint explains a 403, which for this client almost never means the
// token is wrong. The bare status says "forbidden" and points at the
// credential, which is the wrong place to look.
//
// Opening a pull request gets its own answer. It is refused even with
// pull-requests: write, by a repository setting no workflow file can
// grant — and a hint naming only the permissions block sends you to a
// block that is already correct, which cost a debugging round.
func hint(status int, method, path string) string {
	if status != http.StatusForbidden {
		return ""
	}
	if method == http.MethodPost && strings.HasSuffix(pathOnly(path), "/pulls") {
		return " — opening a PR needs BOTH pull-requests: write on the workflow AND" +
			" Settings → Actions → General → \"Allow GitHub Actions to create and approve pull requests\"" +
			" on the repository; the second is not grantable from a workflow file"
	}
	return " — 403 is a permissions answer, but which permissions depends on which token the run was" +
		" given: under GITHUB_TOKEN it is the calling workflow's permissions: block, which drops every" +
		" permission it does not name; under AGENT_GITHUB_TOKEN it is that token's own repository" +
		" permissions, which no workflow file can widen. Naming only the first sent a rework run to a" +
		" permissions: block that was already correct"
}

// apiMessage returns GitHub's own explanation for a failed response.
// That explanation is the whole difference between two 403s that are
// otherwise identical on the wire — "Resource not accessible by personal
// access token" and "Resource not accessible by integration" point at
// different credentials. Discarding the body left callers with a bare
// "403 Forbidden" and a guess, and the guess cost a full rework run.
//
// Bounded, because an error body is not a payload; the caller has
// already given up on the response by the time this is reached.
func apiMessage(r *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil || len(raw) == 0 {
		return ""
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Message != "" {
		return ": " + body.Message
	}
	return ": " + strings.TrimSpace(string(raw))
}

// pathOnly trims a query string so a suffix match is not defeated by one.
func pathOnly(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}

// ActingLogin reports the login this token acts as, or "" when it is
// an Actions token rather than a user's.
//
// The distinction is not cosmetic. A GITHUB_TOKEN cannot read /user at
// all — GitHub answers "Resource not accessible by integration" — and
// that same not-a-user status is why events it creates start no
// workflow runs and why PRs it opens need a human's approval. So the
// call that fails is a direct test of the property that matters, not a
// proxy for it.
func (c *Client) ActingLogin(ctx context.Context) (string, error) {
	var data struct {
		Login string `json:"login"`
	}
	if err := c.rest(ctx, http.MethodGet, "/user", nil, &data); err != nil {
		// Forbidden here means "this is an Actions token", which is an
		// answer rather than a failure. Anything else is a real error.
		if strings.Contains(err.Error(), "HTTP 403") {
			return "", nil
		}
		return "", err
	}
	return data.Login, nil
}

func (c *Client) DispatchWorkflow(ctx context.Context, workflowFile string, inputs map[string]string) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/dispatches", c.owner, c.repo, url.PathEscape(workflowFile))
	return c.rest(ctx, http.MethodPost, path, map[string]any{"ref": c.ref, "inputs": inputs}, nil)
}

// ListAgentRuns returns the agent runs the repository knows about.
//
// **One page per agent workflow, not one page of the repository.** The
// correlation only needs the latest run per ticket, and the justification
// this call used to carry — "anything past 100 runs ago is not it" —
// assumed agent runs are most of what the repository runs. They are not.
// The sweep is a workflow run too, and it is woken by CI completions as
// well as by the metronome, so its volume rises with exactly the activity
// that produces agent runs. Measured on orchestration-dummy, 2026-08-23:
// the 29 most recent runs are all `pipeline: sweep`, five of them inside
// fifteen minutes around a single merge.
//
// A live agent run crowded off page 1 reads as no run at all, so
// `awaitingDispatch` answers "never dispatched" and the sweep starts a
// second one. On Catapult's ORC-45 that was two boundary agents on one
// ticket, about twenty-two minutes of model spend. `core.VerifyPickup`
// now aborts the duplicate at claim time, which made this residual rather
// than urgent — but a window measured in *runs*, on a repository whose run
// count is dominated by a per-event metronome, has no lower bound in time.
//
// Per workflow file the window is what it claims to be: the singular
// agents run one at a time, so 100 runs of `pipeline-agent-dev.yml` is a
// long history rather than a few busy minutes. The cost is one API call
// per wired agent kind per snapshot rather than one per snapshot.
func (c *Client) ListAgentRuns(ctx context.Context) ([]host.AgentRun, error) {
	if len(c.agentWorkflows) == 0 {
		// A client built without the list still works, but says so. A
		// silent fallback would restore the blind spot above for any
		// caller that forgot the option, and what it produces — a second
		// agent on one ticket — does not look like a missing option from
		// anywhere the symptom appears.
		fmt.Fprintln(os.Stderr, "pipeline: no agent workflow files given, reading the repository's runs unfiltered — "+
			"a busy repository can crowd a live agent run out of that window (config `agents`)")
		return c.agentRunsAt(ctx, fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=100", c.owner, c.repo))
	}
	var out []host.AgentRun
	for _, f := range c.agentWorkflows {
		runs, err := c.agentRunsAt(ctx, fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?per_page=100",
			c.owner, c.repo, url.PathEscape(f)))
		if err != nil {
			// Fatal, and the 404 that means "no such workflow file"
			// included. This is a project-wide read, which attachHostFacts
			// takes as fatal for the reason that applies here too: a kind
			// whose runs cannot be listed reads as a kind that is idle,
			// and an idle kind is an invitation to dispatch. A renamed or
			// mistyped `agents` entry is one of the causes the stale-claim
			// comment already sends the author looking for, and it is
			// better named here, once, than inferred there.
			return nil, fmt.Errorf("github: agent workflow %q: %w", f, err)
		}
		out = append(out, runs...)
	}
	return out, nil
}

// agentRunsAt reads one runs listing and keeps the runs whose name follows
// the correlation convention. A workflow file may hold runs that are not
// agent runs — a manual re-run of the stub, say — so the name filter
// stays: it is also where the kind and the ticket key come from.
func (c *Client) agentRunsAt(ctx context.Context, path string) ([]host.AgentRun, error) {
	var data struct {
		WorkflowRuns []struct {
			ID           int64     `json:"id"`
			DisplayTitle string    `json:"display_title"`
			Status       string    `json:"status"`
			UpdatedAt    time.Time `json:"updated_at"`
			HTMLURL      string    `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return nil, err
	}
	var out []host.AgentRun
	for _, r := range data.WorkflowRuns {
		m := runNameRe.FindStringSubmatch(r.DisplayTitle)
		if m == nil {
			continue
		}
		out = append(out, host.AgentRun{
			ID: fmt.Sprintf("%d", r.ID), Kind: m[1], TicketKey: m[2],
			Live:    r.Status != "completed",
			EndedAt: r.UpdatedAt,
			URL:     r.HTMLURL,
		})
	}
	return out, nil
}

func (c *Client) ListOpenPRs(ctx context.Context) ([]host.PR, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=100", c.owner, c.repo)
	var data []struct {
		Number  int    `json:"number"`
		Draft   bool   `json:"draft"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return nil, err
	}
	out := make([]host.PR, 0, len(data))
	for _, p := range data {
		out = append(out, host.PR{
			Number: p.Number, Branch: p.Head.Ref, HeadSHA: p.Head.SHA,
			Draft: p.Draft, URL: p.HTMLURL,
		})
	}
	return out, nil
}

// MergeStateFor reads the `mergeable` field, which only the single-PR
// response carries.
//
// MergePR deliberately does not pre-check this, and the reason there
// still holds: the field is computed asynchronously, so a pre-check
// races a merge that would have succeeded. This call is not that. It
// diagnoses a ticket that has been sitting in Checks — where the
// question is not "may I merge now" but "is this branch ever going to
// get a verdict", and by the time a sweep asks, GitHub computed the
// answer long ago. `null` is still reported as unknown and acted on by
// nobody.
func (c *Client) MergeStateFor(ctx context.Context, number int) (host.MergeState, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", c.owner, c.repo, number)
	var data struct {
		Mergeable *bool `json:"mergeable"`
		// "dirty" is GitHub's word for a conflict. Read as well as
		// `mergeable`, because the two are not the same question:
		// mergeable_state also carries "blocked" (a required check is
		// failing) and "behind", neither of which is a conflict.
		MergeableState string `json:"mergeable_state"`
	}
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return host.MergeUnknown, err
	}
	if data.MergeableState == "dirty" {
		return host.MergeConflicted, nil
	}
	if data.Mergeable == nil {
		return host.MergeUnknown, nil
	}
	if !*data.Mergeable {
		return host.MergeConflicted, nil
	}
	return host.MergeClean, nil
}

// RerunRun re-runs every job of a completed run, keeping the run's id
// and its original event — which is the reason it is a re-run and not a
// fresh dispatch. `workflow_dispatch` would fail three ways over: a
// project's ci.yml declares `on: pull_request` and would not accept it;
// outside a pull_request event `github.head_ref` is empty, so the
// audit's own gate skips and the run reports a green that checked
// nothing; and runsForSHA drops `workflow_dispatch` runs on purpose, so
// the verdict would never be read even if it were right.
func (c *Client) RerunRun(ctx context.Context, runID int64) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/rerun", c.owner, c.repo, runID)
	return c.rest(ctx, http.MethodPost, path, nil, nil)
}

func (c *Client) ChecksFor(ctx context.Context, headSHA string) (host.Checks, error) {
	runs, err := c.runsForSHA(ctx, headSHA)
	if err != nil {
		return host.Checks{}, err
	}
	if len(runs) == 0 {
		return host.Checks{Status: host.ChecksNone}, nil
	}
	agg := host.Checks{Status: host.ChecksGreen}
	red := host.Checks{Status: host.ChecksRed}
	for _, r := range runs {
		if r.Status != "completed" {
			if agg.Status == host.ChecksGreen {
				agg.Status = host.ChecksPending
			}
			continue
		}
		if !failedConclusion(r.Conclusion) {
			continue
		}
		// The link stays the first failing run, which is what the
		// marker has always carried.
		if red.RunURL == "" {
			red.RunURL = r.HTMLURL
			red.RunID, red.RunAttempt = r.ID, r.RunAttempt
		}
		// Every failing job is named, not just the first — a build that
		// broke three jobs is a different fact from one that broke one,
		// and the agent reading this is deciding what to fix. Jobs
		// rather than the workflow's name because that is the grain the
		// check-runs API gave and the rework prompt was written against.
		jobs, err := c.runJobs(ctx, r.ID)
		if err != nil {
			// Named at workflow grain rather than dropped: "ci failed"
			// is worse than "ci / test failed" and far better than a
			// red verdict with nothing named.
			red.FailedJobs = append(red.FailedJobs, r.Name)
			continue
		}
		for _, j := range jobs {
			if j.Status == "completed" && failedConclusion(j.Conclusion) {
				red.FailedJobs = append(red.FailedJobs, j.Name)
			}
		}
	}
	if red.RunURL != "" {
		return red, nil
	}
	return agg, nil
}

// workflowRun is one Actions run as the CI verdict reads it.
type workflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	// RunAttempt separates one verdict from the next on the same run.
	// The id and the URL do not: a re-run keeps both.
	RunAttempt int `json:"run_attempt"`
}

// runsForSHA lists the Actions runs that judged a commit.
//
// The obvious endpoint for "is this commit green" is
// `/commits/{sha}/check-runs`, and this used to call it. **A
// fine-grained token cannot be granted the check-runs API at all** —
// the endpoint appears nowhere in GitHub's fine-grained permissions
// reference, and "Commit statuses" is a different API. So that call
// answers 403 for a reason no scope can fix, under any token that is
// not a workflow's own GITHUB_TOKEN. SETUP.md has said so for a while.
//
// What it did not say is that this function is on the agents' path.
// Every claim builds a full project snapshot, and the snapshot reads a
// CI verdict for every ticket sitting in Checks — under
// AGENT_GITHUB_TOKEN, which is exactly the token that cannot make the
// call. Measured on Catapult: ORC-7's design claim died 35 seconds in
// on a 403 reading ORC-5's check runs, a ticket it had no interest in,
// and sat in Designing until the stale-claim grace expired 23 minutes
// later. The sweep had read the same verdict successfully minutes
// earlier, under GITHUB_TOKEN, which is what made it look intermittent.
//
// The Actions API answers the same question under a permission a PAT
// can hold, and FailedJobLogs already reads CI this way. The tradeoff
// is real and worth naming: this sees workflow runs in this repository
// and not third-party checks, so a project whose gates run outside
// Actions needs another reader. Every project here runs its gates in
// Actions.
func (c *Client) runsForSHA(ctx context.Context, headSHA string) ([]workflowRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?head_sha=%s&per_page=100", c.owner, c.repo, headSHA)
	var data struct {
		WorkflowRuns []workflowRun `json:"workflow_runs"`
	}
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return nil, err
	}
	out := make([]workflowRun, 0, len(data.WorkflowRuns))
	for _, r := range data.WorkflowRuns {
		// The pipeline's own machinery is not this commit's verdict.
		// Agent workflows are dispatched and the sweep is scheduled, and
		// both run against a ref whose head can coincide with a
		// ticket's — at which point an agent run's own status would
		// decide whether the ticket's gates passed. check-runs could not
		// confuse the two; this endpoint can.
		if r.Event == "workflow_dispatch" || r.Event == "schedule" {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Bounds on what a failing build may put in a prompt. Three jobs and a
// tail each, because the tail is where a runner puts the failure and the
// head is where it puts the setup — and because an unbounded log is an
// unbounded prompt, which fails the run in a way that looks like the
// model's fault.
const (
	maxFailedJobs   = 3
	maxLogTailLines = 150
	maxLogTailBytes = 20000
)

// FailedJobLogs reads the Actions API, and was the first call here to
// do so — it runs at claim time under AGENT_GITHUB_TOKEN, and a
// fine-grained PAT has no check-runs permission to grant: the endpoint
// appears nowhere in GitHub's fine-grained permissions reference, so no
// setting fixes it. The first rework run to need a log got a bare 403
// and worked the ticket blind.
//
// ChecksFor has since moved to the same API for the same reason, so
// this is no longer the exception it was written as — see runsForSHA
// for the failure that finished the argument.
//
// Actions runs and jobs are grantable (`Actions`, which agent tokens
// already hold to dispatch), and they are the only thing with a log to
// read anyway. Third-party checks are lost here and not missed: the
// ci-red marker already names every failing check, from ChecksFor.
func (c *Client) FailedJobLogs(ctx context.Context, headSHA string) ([]host.JobLog, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?head_sha=%s&per_page=100", c.owner, c.repo, headSHA)
	var runs struct {
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := c.rest(ctx, http.MethodGet, path, nil, &runs); err != nil {
		return nil, err
	}
	var out []host.JobLog
	for _, r := range runs.WorkflowRuns {
		if r.Status != "completed" || !failedConclusion(r.Conclusion) {
			continue
		}
		jobs, err := c.runJobs(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for _, j := range jobs {
			if j.Status != "completed" || !failedConclusion(j.Conclusion) {
				continue
			}
			if len(out) >= maxFailedJobs {
				return out, nil
			}
			entry := host.JobLog{Name: j.Name, URL: j.HTMLURL}
			log, err := c.jobLog(ctx, strconv.FormatInt(j.ID, 10))
			if err != nil {
				// Named without a log rather than dropped: "this job failed
				// and I could not read it" is a fact the agent needs, and
				// silence would read as "it passed".
				entry.Log = fmt.Sprintf("(could not read this job's log: %v)", err)
			} else {
				entry.Log = tail(log, maxLogTailLines, maxLogTailBytes)
			}
			out = append(out, entry)
		}
	}
	return out, nil
}

// failedConclusion reports whether a completed run or job counts as red.
// failure, timed_out, cancelled and action_required all do.
func failedConclusion(conclusion string) bool {
	switch conclusion {
	case "success", "neutral", "skipped":
		return false
	}
	return true
}

// workflowJob is one Actions job as FailedJobLogs reads it.
type workflowJob struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

func (c *Client) runJobs(ctx context.Context, runID int64) ([]workflowJob, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=100", c.owner, c.repo, runID)
	var data struct {
		Jobs []workflowJob `json:"jobs"`
	}
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return nil, err
	}
	return data.Jobs, nil
}

// jobLog fetches one job's log. The endpoint answers with a redirect to
// plain text rather than JSON, so it does not go through rest().
func (c *Client) jobLog(ctx context.Context, jobID string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/jobs/%s/logs", c.baseURL, c.owner, c.repo, jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// This error is read by a model, not by a maintainer with the repo
		// open: it lands verbatim in the rework prompt as the reason the
		// build's logs are missing. "403 Forbidden" told one dev agent
		// nothing it could act on, so it worked the ticket log-blind.
		return "", fmt.Errorf("job log %s: %s%s%s", jobID, resp.Status,
			apiMessage(resp), hint(resp.StatusCode, http.MethodGet, url))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// tail returns the last lines of s, under both a line and a byte bound.
func tail(s string, lines, bytes int) string {
	if len(s) > bytes {
		s = s[len(s)-bytes:]
		// The first line is now cut mid-way; drop it rather than present
		// a fragment as a line.
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	split := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(split) > lines {
		split = split[len(split)-lines:]
	}
	return strings.Join(split, "\n")
}

func (c *Client) CreatePR(ctx context.Context, branch, title, body string, draft bool) (host.PR, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls", c.owner, c.repo)
	var data struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	req := map[string]any{"title": title, "body": body, "head": branch, "base": c.ref, "draft": draft}
	if err := c.rest(ctx, http.MethodPost, path, req, &data); err != nil {
		return host.PR{}, err
	}
	return host.PR{Number: data.Number, Branch: branch, HeadSHA: data.Head.SHA, Draft: draft, URL: data.HTMLURL}, nil
}

// MergePR squash-merges: one ticket, one commit on main, and the ancestry
// check works the same either way.
func (c *Client) MergePR(ctx context.Context, number int) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", c.owner, c.repo, number)
	var data struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	if err := c.rest(ctx, http.MethodPut, path, map[string]any{"merge_method": "squash"}, &data); err != nil {
		// 405 is GitHub's answer for "not mergeable"; 409 is the same
		// answer when the head moved between the check and the merge.
		// Recognised here rather than pre-checked with the `mergeable`
		// field, which GitHub computes asynchronously and reports as
		// null until it is ready — a pre-check races that, and the merge
		// attempt does not.
		if strings.Contains(err.Error(), "HTTP 405") || strings.Contains(err.Error(), "HTTP 409") {
			return "", fmt.Errorf("%w: PR #%d: %v", host.ErrNotMergeable, number, err)
		}
		return "", err
	}
	if !data.Merged {
		return "", fmt.Errorf("%w: PR #%d: GitHub reported it unmerged", host.ErrNotMergeable, number)
	}
	return data.SHA, nil
}

// IsAncestor uses the compare API: base...head with head "ahead of" or
// "identical to" base means base is an ancestor.
func (c *Client) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	if ancestor == descendant {
		return true, nil
	}
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", c.owner, c.repo, ancestor, descendant)
	var data struct {
		Status string `json:"status"`
	}
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return false, err
	}
	return data.Status == "ahead" || data.Status == "identical", nil
}

// RecordDeployment creates a deployment for a commit and immediately
// marks it successful. The dummy's stand-in for a hosting platform
// (PLAN M4): what the pipeline's post-deploy check actually exercises
// is listing deployments and comparing ancestry, and that path is the
// same whether the deployment describes a real rollout or this.
//
// required_contexts is empty on purpose. GitHub otherwise refuses to
// create a deployment until every status check on the commit has
// passed, which for a merge commit on main is a race the caller would
// have to poll around.
func (c *Client) RecordDeployment(ctx context.Context, sha, environment string) error {
	var dep struct {
		ID int `json:"id"`
	}
	create := map[string]any{
		"ref":               sha,
		"environment":       environment,
		"auto_merge":        false,
		"required_contexts": []string{},
	}
	if err := c.rest(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/deployments", c.owner, c.repo), create, &dep); err != nil {
		return fmt.Errorf("creating deployment for %s: %w", sha, err)
	}
	status := map[string]any{"state": "success"}
	path := fmt.Sprintf("/repos/%s/%s/deployments/%d/statuses", c.owner, c.repo, dep.ID)
	if err := c.rest(ctx, http.MethodPost, path, status, nil); err != nil {
		return fmt.Errorf("marking deployment %d successful: %w", dep.ID, err)
	}
	return nil
}

// ReadFile fetches a file from the dispatch ref via the contents API.
// A 404 is "not there", not a failure: the boundary's first pass over a
// milestone reads a retro note nobody has written yet.
func (c *Client) ReadFile(ctx context.Context, path string) (string, bool, error) {
	body, sha, err := c.readFile(ctx, path)
	_ = sha
	if err != nil {
		return "", false, err
	}
	if body == nil {
		return "", false, nil
	}
	return string(body), true, nil
}

// readFile returns the content and the blob sha, both nil/empty when the
// file is absent. The sha is what turns a create into a replace, so the
// two always come from the same read — a stale sha is a 409, and one
// fetched separately can go stale between the calls.
func (c *Client) readFile(ctx context.Context, path string) ([]byte, string, error) {
	getPath := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", c.owner, c.repo, path, c.ref)
	var meta struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
	}
	err := c.rest(ctx, http.MethodGet, getPath, nil, &meta)
	if err != nil {
		if isNotFound(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	if meta.Encoding != "base64" {
		return nil, "", fmt.Errorf("github: %s came back %q-encoded, not base64", path, meta.Encoding)
	}
	// The contents API wraps base64 at 60 columns; the standard decoder
	// rejects the newlines.
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(meta.Content, "\n", ""))
	if err != nil {
		return nil, "", fmt.Errorf("github: decoding %s: %w", path, err)
	}
	return raw, meta.SHA, nil
}

// PutFile commits one file to the dispatch ref, creating it or replacing
// what is there.
func (c *Client) PutFile(ctx context.Context, path, content, message string) error {
	existing, sha, err := c.readFile(ctx, path)
	if err != nil {
		return err
	}
	if existing != nil && string(existing) == content {
		// Nothing to say. A commit per re-run would fill the log of a
		// resumed boundary with empty retro-note commits.
		return nil
	}
	putPath := fmt.Sprintf("/repos/%s/%s/contents/%s", c.owner, c.repo, path)
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  c.ref,
	}
	if sha != "" {
		body["sha"] = sha
	}
	return c.rest(ctx, http.MethodPut, putPath, body, nil)
}

// MarkPRReady flips the draft flag off. REST cannot do this; it is a
// GraphQL-only mutation, keyed by the PR's node id.
func (c *Client) MarkPRReady(ctx context.Context, number int) error {
	var pr struct {
		NodeID string `json:"node_id"`
		Draft  bool   `json:"draft"`
	}
	if err := c.rest(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", c.owner, c.repo, number), nil, &pr); err != nil {
		return err
	}
	if !pr.Draft {
		return nil
	}
	q := map[string]any{
		"query":     `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { clientMutationId } }`,
		"variables": map[string]any{"id": pr.NodeID},
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/graphql", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: graphql ready-for-review: HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("github: ready-for-review: %s", envelope.Errors[0].Message)
	}
	return nil
}
