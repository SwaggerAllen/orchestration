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
		return fmt.Errorf("github: %s %s: HTTP %d%s", method, path, resp.StatusCode, hint(resp.StatusCode))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// hint explains a 403, which for this client almost never means the
// token is wrong. A workflow that declares a permissions: block drops
// every permission it does not list to none, so adding one call to the
// sweep can 403 on a token that is otherwise fine. The bare status says
// "forbidden" and points at the credential — the wrong place to look.
func hint(status int) string {
	if status != http.StatusForbidden {
		return ""
	}
	return " — check the calling workflow's permissions: block, which drops every permission it does not name"
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
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", c.owner, c.repo, headSHA)
	var data struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return host.Checks{}, err
	}
	if len(data.CheckRuns) == 0 {
		return host.Checks{Status: host.ChecksNone}, nil
	}
	agg := host.Checks{Status: host.ChecksGreen}
	for _, r := range data.CheckRuns {
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
			// is not green, and the failing run is the one to link.
			return host.Checks{Status: host.ChecksRed, RunURL: r.HTMLURL}, nil
		}
	}
	return agg, nil
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
