// Package ghdeploy implements the deploy port against the GitHub
// Deployments API. The dummy project uses it so dry runs exercise the
// pipeline's real post-deploy logic — ancestry comparison included —
// without a real hosting platform (PLAN M4): its CI records a deployment
// per merge, and this adapter reads them back exactly as the DO adapter
// reads App Platform.
package ghdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/deploy"
)

// Client implements deploy.Deploy for one repo + environment.
type Client struct {
	baseURL     string
	owner, repo string
	environment string
	token       string
	http        *http.Client
}

var _ deploy.Deploy = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at a test server.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// New builds a client. environment is the config's deploy.endpoint value
// for this provider (e.g. "production").
func New(repository, token, environment string, opts ...Option) (*Client, error) {
	owner, repo, ok := cutRepo(repository)
	if !ok {
		return nil, fmt.Errorf("ghdeploy: repository %q is not owner/repo", repository)
	}
	c := &Client{
		baseURL: "https://api.github.com", owner: owner, repo: repo,
		environment: environment, token: token,
		http: &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

func cutRepo(s string) (string, string, bool) {
	for i := range s {
		if s[i] == '/' {
			return s[:i], s[i+1:], s[:i] != "" && s[i+1:] != ""
		}
	}
	return "", "", false
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ghdeploy: GET %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) State(ctx context.Context) (*deploy.State, error) {
	var deployments []struct {
		ID  int64  `json:"id"`
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/%s/deployments?environment=%s&per_page=10", c.owner, c.repo, c.environment)
	if err := c.get(ctx, path, &deployments); err != nil {
		return nil, err
	}

	// Newest first. The newest finished attempt decides Failed; the
	// newest successful one is what's "serving" — the same shape the DO
	// adapter reports, so the plane can't tell the providers apart.
	out := &deploy.State{}
	for i, d := range deployments {
		var statuses []struct {
			State string `json:"state"`
		}
		sp := fmt.Sprintf("/repos/%s/%s/deployments/%d/statuses?per_page=1", c.owner, c.repo, d.ID)
		if err := c.get(ctx, sp, &statuses); err != nil {
			return nil, err
		}
		if len(statuses) == 0 {
			continue // no status yet: in flight
		}
		switch statuses[0].State {
		case "success":
			if out.ActiveSHA == "" {
				out.ActiveSHA = d.SHA
			}
		case "failure", "error":
			if i == 0 {
				out.Failed = true
				out.FailedSHA = d.SHA
			}
		}
		if out.ActiveSHA != "" && i > 0 {
			break
		}
	}
	return out, nil
}
