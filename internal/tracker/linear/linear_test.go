package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// fakeLinear pins the wire shape: it asserts auth and content-type on every
// request, routes on the GraphQL operation, and lets each test script the
// data it returns.
func fakeLinear(t *testing.T, handle func(query string, vars map[string]any) (any, []gqlError)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "lin_api_test" {
			t.Errorf("Authorization = %q, want bare key", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var req gqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		data, errs := handle(req.Query, req.Variables)
		resp := map[string]any{}
		if data != nil {
			resp["data"] = data
		}
		if errs != nil {
			resp["errors"] = errs
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
}

func TestListStates(t *testing.T) {
	srv := fakeLinear(t, func(query string, vars map[string]any) (any, []gqlError) {
		if !strings.Contains(query, "workflowStates") {
			t.Errorf("unexpected query: %s", query)
		}
		if vars["teamId"] != "team_1" {
			t.Errorf("teamId var = %v", vars["teamId"])
		}
		return map[string]any{"workflowStates": map[string]any{
			"nodes": []map[string]any{
				{"id": "s1", "name": "Designing", "type": "started"},
				{"id": "s2", "name": "Done", "type": "completed"},
			},
			"pageInfo": map[string]any{"hasNextPage": false},
		}}, nil
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	states, err := c.ListStates(context.Background(), "team_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].Name != "Designing" || states[0].Category != protocol.CategoryStarted {
		t.Errorf("states = %+v", states)
	}
}

func TestCreateStateSendsCategoryAndColor(t *testing.T) {
	srv := fakeLinear(t, func(query string, vars map[string]any) (any, []gqlError) {
		if !strings.Contains(query, "workflowStateCreate") {
			t.Errorf("unexpected query: %s", query)
		}
		input := vars["input"].(map[string]any)
		if input["teamId"] != "team_1" || input["name"] != "Reconciling" || input["type"] != "started" {
			t.Errorf("input = %v", input)
		}
		// Per state, not per category: nine states share `started`, and
		// one colour across all nine makes the board unreadable.
		if got, want := input["color"], protocol.Colors[protocol.Reconciling]; got != want {
			t.Errorf("color = %v, want %v (the state's own colour)", got, want)
		}
		return map[string]any{"workflowStateCreate": map[string]any{
			"success":       true,
			"workflowState": map[string]any{"id": "s9", "name": "Reconciling", "type": "started", "color": protocol.Colors[protocol.Reconciling]},
		}}, nil
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	s, err := c.CreateState(context.Background(), "team_1", tracker.NewState{Name: "Reconciling", Category: protocol.CategoryStarted, Color: protocol.Colors[protocol.Reconciling]})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "s9" {
		t.Errorf("state = %+v", s)
	}
}

func TestListAndCreateLabels(t *testing.T) {
	srv := fakeLinear(t, func(query string, vars map[string]any) (any, []gqlError) {
		switch {
		case strings.Contains(query, "issueLabelCreate"):
			input := vars["input"].(map[string]any)
			return map[string]any{"issueLabelCreate": map[string]any{
				"success":    true,
				"issueLabel": map[string]any{"id": "l9", "name": input["name"]},
			}}, nil
		case strings.Contains(query, "issueLabels"):
			// Workspace labels carry no team, so a bare team-equality
			// filter omits them and setup plans a create Linear rejects.
			if !strings.Contains(query, "team: {null: true}") {
				t.Errorf("label query must reach workspace-scoped labels too:\n%s", query)
			}
			return map[string]any{"issueLabels": map[string]any{
				"nodes":    []map[string]any{{"id": "l1", "name": "bug"}},
				"pageInfo": map[string]any{"hasNextPage": false},
			}}, nil
		default:
			t.Errorf("unexpected query: %s", query)
			return nil, nil
		}
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	labels, err := c.ListLabels(context.Background(), "team_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 1 || labels[0].Name != "bug" {
		t.Errorf("labels = %+v", labels)
	}
	l, err := c.CreateLabel(context.Background(), "team_1", "re-evaluate")
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "l9" || l.Name != "re-evaluate" {
		t.Errorf("label = %+v", l)
	}
}

func TestGraphQLErrorsSurface(t *testing.T) {
	srv := fakeLinear(t, func(string, map[string]any) (any, []gqlError) {
		return nil, []gqlError{{Message: "team not found"}}
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	if _, err := c.ListStates(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "team not found") {
		t.Errorf("want GraphQL error surfaced, got %v", err)
	}
}

func TestFullPageIsAnError(t *testing.T) {
	nodes := make([]map[string]any, 0, pageSize)
	for i := 0; i < pageSize; i++ {
		nodes = append(nodes, map[string]any{"id": fmt.Sprintf("s%d", i), "name": fmt.Sprintf("S%d", i), "type": "started"})
	}
	srv := fakeLinear(t, func(string, map[string]any) (any, []gqlError) {
		return map[string]any{"workflowStates": map[string]any{
			"nodes":    nodes,
			"pageInfo": map[string]any{"hasNextPage": true},
		}}, nil
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	if _, err := c.ListStates(context.Background(), "team_1"); err == nil {
		t.Error("want error on overflowing page, got nil")
	}
}
