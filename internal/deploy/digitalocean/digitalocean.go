// Package digitalocean implements the deploy port against the App
// Platform deployments API. The config's deploy.endpoint is the full
// deployments URL for the app; the token comes from the environment.
package digitalocean

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/deploy"
)

// Client implements deploy.Deploy.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

var _ deploy.Deploy = (*Client)(nil)

func New(endpoint, token string) *Client {
	return &Client{endpoint: endpoint, token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// phases that mean "this attempt is finished and failed". Everything
// in-flight (QUEUED, BUILDING, DEPLOYING, ...) is neither active nor
// failed — the ticket stays pending and the timeout is the backstop.
var failedPhases = map[string]bool{"ERROR": true, "CANCELED": true}

func (c *Client) State(ctx context.Context) (*deploy.State, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("digitalocean: HTTP %d from deployments API", resp.StatusCode)
	}
	var data struct {
		Deployments []struct {
			Phase    string `json:"phase"`
			Services []struct {
				SourceCommitHash string `json:"source_commit_hash"`
			} `json:"services"`
		} `json:"deployments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("digitalocean: decoding deployments: %w", err)
	}

	// Deployments come newest first. The newest finished attempt decides
	// Failed; the newest ACTIVE one is what's serving.
	out := &deploy.State{}
	for i, d := range data.Deployments {
		sha := ""
		if len(d.Services) > 0 {
			sha = d.Services[0].SourceCommitHash
		}
		switch {
		case d.Phase == "ACTIVE" && out.ActiveSHA == "":
			out.ActiveSHA = sha
		case i == 0 && failedPhases[d.Phase]:
			out.Failed = true
			out.FailedSHA = sha
		}
	}
	return out, nil
}
