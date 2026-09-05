// Package linear implements the tracker port against Linear's GraphQL API.
// Linear ships no Go SDK, so the queries are written by hand (PLAN §1);
// each method carries exactly the query it needs, and the httptest-backed
// tests pin the wire shape so a drifting query fails here rather than in a
// dry run.
//
// M0 implements the setup slice (states and labels). Issues, comments and
// transitions arrive with M2.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// DefaultEndpoint is Linear's GraphQL endpoint.
const DefaultEndpoint = "https://api.linear.app/graphql"

// pageSize is Linear's maximum. One team holds nowhere near 250 states or
// labels, so setup reads a single page and treats a full page as an error
// rather than paginating code that could never be exercised.
const pageSize = 250

// Client implements tracker.Tracker.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

var _ tracker.Tracker = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithEndpoint points the client elsewhere — the httptest server in tests.
func WithEndpoint(url string) Option {
	return func(c *Client) { c.endpoint = url }
}

// WithHTTPClient replaces the underlying HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New builds a client from a personal API key. Linear personal keys go in
// the Authorization header bare, without a Bearer prefix.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		endpoint: DefaultEndpoint,
		apiKey:   apiKey,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message string `json:"message"`
}

// Retry bounds for reads. A blip here takes a whole sweep down, and the
// sweep's very first act is a read — so one unlucky request costs every
// dispatch, promotion and escalation that beat would have made.
//
// Measured on Catapult, 2026-09-05: six sweeps failed between 14:00 and
// 15:20 with `Post "https://api.linear.app/graphql": context deadline
// exceeded`, each 40–51s of wall clock, and the one read in full shows
// 30.04s between the step starting and the error with no output in
// between — the client timeout below, firing on the first call, before
// anything was written.
//
// **The attempt count and the delay are bounds, not measurements.** The
// only figure taken is that 30s timeout; nobody here has measured how
// long Linear stays unreachable, so three attempts is a guess at "long
// enough for a blip, short enough that a real outage still fails the
// run" and is written as one. Worst case is three timeouts plus backoff,
// a little over ninety seconds, against a sweep that otherwise takes
// well under a minute.
const (
	maxReadAttempts = 3
	readBackoff     = 500 * time.Millisecond
)

// isRead reports whether a GraphQL document is a query rather than a
// mutation, which is what decides whether it may be retried.
//
// Read off the document itself rather than from a list of method names
// beside it: a list is a second place to update, and the one thing worse
// than not retrying is retrying a write. A new mutation is excluded here
// by being what it is.
func isRead(query string) bool {
	return strings.HasPrefix(strings.TrimSpace(query), "query")
}

// retryableStatus reports whether an HTTP status is worth another go.
// 5xx and 429 are the server's problem and may pass; a 4xx is this
// client's request and will fail identically however often it is sent.
func retryableStatus(code int) bool {
	return code >= 500 || code == http.StatusTooManyRequests
}

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return err
	}
	attempts := 1
	// **Writes are never retried, and that is the deliberate half.** A
	// timeout awaiting headers says nothing about whether the server
	// processed the request, so a retried mutation may be a second
	// comment or a second transition. A duplicate comment is not merely
	// noise: the escalation rules count their own markers
	// (`len(markersOf(t, marker.CIRed)) + 1`), so one duplicated ci-red
	// marker parks a ticket in Blocked a failure early. The sweep is
	// convergent — the same stance `internal/state`'s worker already
	// takes on its write path — so a failed write costs a beat and the
	// next sweep re-derives it, while a duplicated write is not
	// recoverable at all.
	if isRead(query) {
		attempts = maxReadAttempts
	}
	var lastErr error
	started := time.Now()
	for attempt := 1; ; attempt++ {
		// Rebuilt every attempt, not reused: the request body is a
		// reader and the first attempt drains it, so a retried request
		// built once would POST an empty document and be rejected as a
		// syntax error — a different failure wearing the same clothes.
		lastErr = c.attempt(ctx, body, out)
		var retry retryableError
		if !errors.As(lastErr, &retry) || attempt >= attempts {
			break
		}
		// Linear-ish backoff with the caller's deadline respected. A
		// context that is already done must not sleep on it: the sweep
		// has a job timeout and burning it here helps nobody.
		delay := time.Duration(attempt) * readBackoff
		select {
		case <-ctx.Done():
			return fmt.Errorf("linear: %w (gave up after %d attempt(s) over %s)",
				lastErr, attempt, time.Since(started).Round(time.Millisecond))
		case <-time.After(delay):
		}
	}
	if lastErr != nil && attempts > 1 {
		// The count is in the message or a retried failure reads as a
		// single one, and the next reader measures the wrong thing.
		var retry retryableError
		if errors.As(lastErr, &retry) {
			return fmt.Errorf("linear: %w (after %d attempts over %s)",
				lastErr, attempts, time.Since(started).Round(time.Millisecond))
		}
	}
	return lastErr
}

// retryableError marks the failures worth another attempt, so the loop
// above decides on a type rather than by re-inspecting strings.
type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

func (c *Client) attempt(ctx context.Context, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		// Transport failures are the measured case: a timeout awaiting
		// headers, a reset connection, a DNS blip.
		return retryableError{err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		httpErr := fmt.Errorf("linear: HTTP %d", resp.StatusCode)
		if retryableStatus(resp.StatusCode) {
			return retryableError{httpErr}
		}
		return httpErr
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("linear: decoding response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("linear: %s", envelope.Errors[0].Message)
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("linear: decoding data: %w", err)
		}
	}
	return nil
}

// fallbackColors are used only when the caller names no color — a state
// that is not one of the pipeline's, which setup meets but does not own.
// The pipeline's own states are coloured per state (protocol.Colors),
// because nine of them share the `started` category and one colour for
// all nine makes the board unreadable.
var fallbackColors = map[protocol.Category]string{
	protocol.CategoryBacklog:   "#bec2c8",
	protocol.CategoryUnstarted: "#e2e2e2",
	protocol.CategoryStarted:   "#f2c94c",
	protocol.CategoryCompleted: "#5e6ad2",
	protocol.CategoryCanceled:  "#95a2b3",
}

// labelColor is the single color for pipeline-created labels; visually
// distinguishing labels is the author's prerogative in the Linear UI.
const labelColor = "#8b5cf6"

func (c *Client) ListStates(ctx context.Context, teamID string) ([]tracker.StateInfo, error) {
	const q = `query States($teamId: ID!, $first: Int!) {
	  workflowStates(filter: {team: {id: {eq: $teamId}}}, first: $first) {
	    nodes { id name type color }
	    pageInfo { hasNextPage }
	  }
	}`
	var data struct {
		WorkflowStates struct {
			Nodes []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Type  string `json:"type"`
				Color string `json:"color"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage bool `json:"hasNextPage"`
			} `json:"pageInfo"`
		} `json:"workflowStates"`
	}
	if err := c.do(ctx, q, map[string]any{"teamId": teamID, "first": pageSize}, &data); err != nil {
		return nil, err
	}
	if data.WorkflowStates.PageInfo.HasNextPage {
		return nil, fmt.Errorf("linear: team %s has over %d workflow states, which the pipeline does not expect", teamID, pageSize)
	}
	out := make([]tracker.StateInfo, 0, len(data.WorkflowStates.Nodes))
	for _, n := range data.WorkflowStates.Nodes {
		out = append(out, tracker.StateInfo{ID: n.ID, Name: n.Name, Category: protocol.Category(n.Type), Color: n.Color})
	}
	return out, nil
}

func (c *Client) CreateState(ctx context.Context, teamID string, ns tracker.NewState) (tracker.StateInfo, error) {
	const q = `mutation CreateState($input: WorkflowStateCreateInput!) {
	  workflowStateCreate(input: $input) {
	    success
	    workflowState { id name type color }
	  }
	}`
	name, category := ns.Name, ns.Category
	color := ns.Color
	if color == "" {
		var ok bool
		if color, ok = fallbackColors[category]; !ok {
			return tracker.StateInfo{}, fmt.Errorf("linear: no color for category %q and none given", category)
		}
	}
	var data struct {
		WorkflowStateCreate struct {
			Success       bool `json:"success"`
			WorkflowState struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Type  string `json:"type"`
				Color string `json:"color"`
			} `json:"workflowState"`
		} `json:"workflowStateCreate"`
	}
	vars := map[string]any{"input": map[string]any{
		"teamId": teamID,
		"name":   name,
		"type":   string(category),
		"color":  color,
	}}
	if err := c.do(ctx, q, vars, &data); err != nil {
		return tracker.StateInfo{}, err
	}
	if !data.WorkflowStateCreate.Success {
		return tracker.StateInfo{}, fmt.Errorf("linear: workflowStateCreate(%q) reported failure", name)
	}
	s := data.WorkflowStateCreate.WorkflowState
	return tracker.StateInfo{ID: s.ID, Name: s.Name, Category: protocol.Category(s.Type), Color: s.Color}, nil
}

// ListLabels returns every label the team can apply: its own, plus the
// workspace-scoped ones. Workspace labels belong to no team, so a
// team-equality filter silently omits them — which reads as "the team has
// no label called frontend" when frontend is right there in the picker.
// Setup then plans a create the API rejects as a duplicate, and label
// attachment fails on a name that exists.
func (c *Client) ListLabels(ctx context.Context, teamID string) ([]tracker.Label, error) {
	const q = `query Labels($teamId: ID!, $first: Int!) {
	  issueLabels(
	    filter: {or: [{team: {id: {eq: $teamId}}}, {team: {null: true}}]},
	    first: $first
	  ) {
	    nodes { id name }
	    pageInfo { hasNextPage }
	  }
	}`
	var data struct {
		IssueLabels struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage bool `json:"hasNextPage"`
			} `json:"pageInfo"`
		} `json:"issueLabels"`
	}
	if err := c.do(ctx, q, map[string]any{"teamId": teamID, "first": pageSize}, &data); err != nil {
		return nil, err
	}
	if data.IssueLabels.PageInfo.HasNextPage {
		return nil, fmt.Errorf("linear: team %s can see over %d labels (its own plus workspace-scoped), which the pipeline does not expect", teamID, pageSize)
	}
	out := make([]tracker.Label, 0, len(data.IssueLabels.Nodes))
	for _, n := range data.IssueLabels.Nodes {
		out = append(out, tracker.Label{ID: n.ID, Name: n.Name})
	}
	return out, nil
}

func (c *Client) CreateLabel(ctx context.Context, teamID, name string) (tracker.Label, error) {
	const q = `mutation CreateLabel($input: IssueLabelCreateInput!) {
	  issueLabelCreate(input: $input) {
	    success
	    issueLabel { id name }
	  }
	}`
	var data struct {
		IssueLabelCreate struct {
			Success    bool `json:"success"`
			IssueLabel struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"issueLabel"`
		} `json:"issueLabelCreate"`
	}
	vars := map[string]any{"input": map[string]any{
		"teamId": teamID,
		"name":   name,
		"color":  labelColor,
	}}
	if err := c.do(ctx, q, vars, &data); err != nil {
		return tracker.Label{}, err
	}
	if !data.IssueLabelCreate.Success {
		return tracker.Label{}, fmt.Errorf("linear: issueLabelCreate(%q) reported failure", name)
	}
	l := data.IssueLabelCreate.IssueLabel
	return tracker.Label{ID: l.ID, Name: l.Name}, nil
}
