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
	"fmt"
	"net/http"
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

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: HTTP %d", resp.StatusCode)
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
