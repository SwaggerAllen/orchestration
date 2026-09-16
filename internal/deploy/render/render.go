// Package render implements the deploy port against Render's deploys
// API. The config's deploy.endpoint is the full deploys URL for the
// service; the token comes from the environment.
//
// Two shape differences from the DigitalOcean adapter beside it, both of
// which fail silently rather than loudly if got wrong, which is why each
// has a test naming it:
//
//   - the response is a bare JSON array of {deploy, cursor} wrappers, not
//     an object with a list inside it. Decoding the wrong one into the
//     right Go type yields an empty list and a *successful* call, which
//     the plane reads as "nothing is serving" — the same answer a healthy
//     service gives before its first deploy.
//   - Render reports a status vocabulary of its own, and the failure half
//     of it is three values rather than one. A failure status left
//     unmapped reads as in-flight, so a Blocked ticket rides to the
//     deploy timeout instead (DESIGN §11, §12).
package render

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

// The status vocabulary, taken from Render's own API reference for
// GET /v1/services/{id}/deploys, read 2026-09-16. It is documentation
// rather than a measurement against a live account — the enumeration is
// a claim about Render, the same kind `tracker.Memory` gets wrong by
// copying the constants beside it (CLAUDE.md), so it is recorded with
// its source and re-checked when a real service is wired in C3's
// second half.
//
// Eleven values, split three ways:
//
//	live                                          -> serving
//	build_failed update_failed pre_deploy_failed
//	canceled                                      -> finished and failed
//	created queued build_in_progress
//	pre_deploy_in_progress update_in_progress     -> in flight
//	deactivated                                   -> was serving, superseded
//
// Only the first two groups are named here. In flight and deactivated are
// both "neither active nor failed", which is what an unmapped status
// already does, so listing them would add a claim without adding
// behaviour — the reasoning the host-to-core outcome mapping records for
// GitHub's unmeasured conclusions.
//
// `canceled` is failed rather than in-flight because Render's own
// zero-downtime rollback lands there: a new instance that fails health
// checks over fifteen minutes is destroyed and the deploy cancelled. A
// ticket whose merge is in that deploy is Blocked, not pending.
var failedStatuses = map[string]bool{
	"build_failed":      true,
	"update_failed":     true,
	"pre_deploy_failed": true,
	"canceled":          true,
}

const liveStatus = "live"

type deployRec struct {
	Status string `json:"status"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
	CreatedAt string `json:"createdAt"`
}

func (c *Client) State(ctx context.Context) (*deploy.State, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("render: HTTP %d from deploys API", resp.StatusCode)
	}
	var page []struct {
		Deploy deployRec `json:"deploy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("render: decoding deploys: %w", err)
	}

	deploys := make([]deployRec, len(page))
	for i, w := range page {
		deploys[i] = w.Deploy
	}

	// Sorted here rather than trusted from the API. The DigitalOcean
	// adapter takes "newest first" from DO's documented ordering; Render's
	// reference does not state one, and nobody here has watched a real
	// account to find out. Ordering decides which deploy sets Failed, so
	// guessing it would be exactly the invented threshold CLAUDE.md
	// warns about. Stable, so deploys whose createdAt does not parse keep
	// the order the API gave them rather than being shuffled among
	// themselves.
	sort.SliceStable(deploys, func(i, j int) bool {
		return createdAt(deploys[i]).After(createdAt(deploys[j]))
	})

	// The newest finished attempt decides Failed; the newest live one is
	// what's serving. A failed deploy on Render leaves the previous
	// instance running, so both halves can be true at once and they name
	// different commits.
	out := &deploy.State{}
	for i, d := range deploys {
		switch {
		case d.Status == liveStatus && out.ActiveSHA == "":
			out.ActiveSHA = d.Commit.ID
		case i == 0 && failedStatuses[d.Status]:
			out.Failed = true
			out.FailedSHA = d.Commit.ID
		}
	}
	return out, nil
}

// createdAt parses the deploy's timestamp, or returns the zero time,
// which sorts last.
func createdAt(d deployRec) time.Time {
	t, err := time.Parse(time.RFC3339, d.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}
