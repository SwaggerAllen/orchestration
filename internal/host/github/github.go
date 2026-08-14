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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
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
	http    *http.Client
}

var _ host.Host = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at a test server.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithDispatchRef sets the branch workflow dispatches run on (default main).
func WithDispatchRef(ref string) Option { return func(c *Client) { c.ref = ref } }

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
		return fmt.Errorf("github: %s %s: HTTP %d%s%s", method, path, resp.StatusCode,
			apiMessage(resp), hint(resp.StatusCode, method, path))
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

func (c *Client) ListAgentRuns(ctx context.Context) ([]host.AgentRun, error) {
	// One page of the most recent runs is enough: correlation only needs
	// the latest run per ticket, and anything past 100 runs ago is not it.
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=100", c.owner, c.repo)
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

func (c *Client) ChecksFor(ctx context.Context, headSHA string) (host.Checks, error) {
	runs, err := c.checkRuns(ctx, headSHA)
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
		switch r.Conclusion {
		case "success", "neutral", "skipped":
		default:
			// failure, timed_out, cancelled, action_required: the branch
			// is not green. Every failing check is named, not just the
			// first — a build that broke three jobs is a different fact
			// from one that broke one, and the agent reading this is
			// deciding what to fix. The link stays the first one, which
			// is what the marker has always carried.
			if red.RunURL == "" {
				red.RunURL = r.HTMLURL
			}
			red.FailedJobs = append(red.FailedJobs, r.Name)
		}
	}
	if red.RunURL != "" {
		return red, nil
	}
	return agg, nil
}

// checkRun is one check run as both ChecksFor and FailedJobLogs read it.
type checkRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

func (c *Client) checkRuns(ctx context.Context, headSHA string) ([]checkRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", c.owner, c.repo, headSHA)
	var data struct {
		CheckRuns []checkRun `json:"check_runs"`
	}
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return nil, err
	}
	return data.CheckRuns, nil
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

func (c *Client) FailedJobLogs(ctx context.Context, headSHA string) ([]host.JobLog, error) {
	runs, err := c.checkRuns(ctx, headSHA)
	if err != nil {
		return nil, err
	}
	var out []host.JobLog
	for _, r := range runs {
		if r.Status != "completed" {
			continue
		}
		switch r.Conclusion {
		case "success", "neutral", "skipped":
			continue
		}
		if len(out) >= maxFailedJobs {
			break
		}
		j := host.JobLog{Name: r.Name, URL: r.HTMLURL}
		// A check run that is not an Actions job — a third-party check —
		// has no log to fetch here. Named without one rather than
		// dropped: "this check failed and I could not read it" is a fact
		// the agent needs, and silence would read as "it passed".
		if id := jobID(r.HTMLURL); id != "" {
			log, err := c.jobLog(ctx, id)
			if err != nil {
				j.Log = fmt.Sprintf("(could not read this job's log: %v)", err)
			} else {
				j.Log = tail(log, maxLogTailLines, maxLogTailBytes)
			}
		}
		out = append(out, j)
	}
	return out, nil
}

// jobIDRe pulls the Actions job id out of a check run's html_url, which
// looks like .../actions/runs/<run>/job/<job>.
var jobIDRe = regexp.MustCompile(`/job/(\d+)`)

func jobID(htmlURL string) string {
	if m := jobIDRe.FindStringSubmatch(htmlURL); m != nil {
		return m[1]
	}
	return ""
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
		return "", err
	}
	if !data.Merged {
		return "", fmt.Errorf("github: PR #%d not merged", number)
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

// PutFileIfAbsent commits one file to the dispatch ref via the contents
// API, unless it already exists there.
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

func (c *Client) PutFileIfAbsent(ctx context.Context, path, content, message string) (bool, error) {
	getPath := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", c.owner, c.repo, path, c.ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+getPath, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return false, nil // already there: re-run safety, not an error
	case http.StatusNotFound:
	default:
		return false, fmt.Errorf("github: checking %s: HTTP %d", path, resp.StatusCode)
	}
	putPath := fmt.Sprintf("/repos/%s/%s/contents/%s", c.owner, c.repo, path)
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  c.ref,
	}
	if err := c.rest(ctx, http.MethodPut, putPath, body, nil); err != nil {
		return false, err
	}
	return true, nil
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
