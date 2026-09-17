// The check-runs API, in its own file deliberately.
//
// `TestNothingReachesTheChecksAPI` scans `github.go` for this endpoint
// and fails on it, because every function there is on the snapshot's
// path — the sweep and every agent claim build a full project snapshot
// under AGENT_GITHUB_TOKEN, and a fine-grained PAT cannot be granted
// check-runs at all. Catapult's ORC-7 died 35 seconds into a design
// claim on a 403 reading another ticket's check runs and sat in
// Designing until the stale-claim grace expired 23 minutes later.
//
// These two methods are the exception, and the file boundary is what
// states it rather than a name on an allowlist: they implement
// `host.CheckRuns`, which is not part of `host.Host`, so nothing holding
// the plane's port can reach them. `TestHostPortExcludesTheCheckRunsAPI`
// asserts that separation, because the file split means nothing without
// it — the reason these are safe is the interface they are *off*, not
// the file they are in.
//
// Their one caller runs inside a workflow job, under that job's own
// GITHUB_TOKEN, which can hold `checks: write`.
package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/host"
)

var _ host.CheckRuns = (*Client)(nil)

// CheckRunsFor lists this commit's check runs whose name starts with
// prefix.
//
// **Only callable under a workflow's own GITHUB_TOKEN.** A fine-grained
// PAT has no check-runs permission to grant, which is why host.CheckRuns
// is a separate interface the plane does not hold — see its comment, and
// runsForSHA for the 403 that finished the argument.
func (c *Client) CheckRunsFor(ctx context.Context, sha, prefix string) ([]host.CheckRun, error) {
	var data struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			Status     string `json:"status"`
			DetailsURL string `json:"details_url"`
			Output     struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
			} `json:"output"`
		} `json:"check_runs"`
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", c.owner, c.repo, sha)
	if err := c.rest(ctx, http.MethodGet, path, nil, &data); err != nil {
		return nil, fmt.Errorf("reading check runs for %s: %w", sha, err)
	}
	var out []host.CheckRun
	for _, r := range data.CheckRuns {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		// An unfinished run carries no conclusion, and reading one as a
		// verdict would have rule 8 compare a result against a blank and
		// report every in-flight judge as a disagreement.
		if r.Status != "completed" {
			continue
		}
		out = append(out, host.CheckRun{
			SHA:        sha,
			Name:       r.Name,
			Conclusion: host.CheckRunConclusion(r.Conclusion),
			Title:      r.Output.Title,
			Summary:    r.Output.Summary,
			DetailsURL: r.DetailsURL,
		})
	}
	return out, nil
}

// CreateCheckRun records a completed verdict. Same token constraint as
// CheckRunsFor.
func (c *Client) CreateCheckRun(ctx context.Context, cr host.CheckRun) error {
	body := map[string]any{
		"name":        cr.Name,
		"head_sha":    cr.SHA,
		"status":      "completed",
		"conclusion":  string(cr.Conclusion),
		"output":      map[string]any{"title": cr.Title, "summary": cr.Summary},
		"details_url": cr.DetailsURL,
	}
	if cr.DetailsURL == "" {
		// GitHub rejects an empty details_url rather than ignoring it,
		// and a verdict has to be recordable even when the evidence link
		// is what is missing — §8.3 rule 3 makes that a *failing* verdict
		// with a reason, not an unrecordable one.
		delete(body, "details_url")
	}
	path := fmt.Sprintf("/repos/%s/%s/check-runs", c.owner, c.repo)
	if err := c.rest(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("recording check run %q on %s: %w", cr.Name, cr.SHA, err)
	}
	return nil
}
