package linear

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

func issueNode(id, key string, overrides map[string]any) map[string]any {
	empty := map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}}
	n := map[string]any{
		"id": id, "identifier": key, "title": "t", "description": "",
		"priority": 3, "createdAt": "2026-01-01T00:00:00Z",
		"state":            map[string]any{"id": "st_1"},
		"projectMilestone": nil,
		"labels":           empty, "comments": empty, "history": empty,
		"relations": empty, "inverseRelations": empty,
	}
	for k, v := range overrides {
		n[k] = v
	}
	return n
}

func issuesPage(nodes []map[string]any, hasNext bool, cursor string) map[string]any {
	return map[string]any{"issues": map[string]any{
		"nodes":    nodes,
		"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
	}}
}

func TestListIssuesHydratesAndPaginates(t *testing.T) {
	calls := 0
	srv := fakeLinear(t, func(query string, vars map[string]any) (any, []gqlError) {
		if !strings.Contains(query, "issues(") {
			t.Errorf("unexpected query: %s", query)
		}
		calls++
		if calls == 1 {
			if vars["after"] != nil {
				t.Errorf("first page should have nil cursor, got %v", vars["after"])
			}
			rich := issueNode("i1", "PIPE-1", map[string]any{
				"projectMilestone": map[string]any{"name": "M: alpha"},
				"labels": map[string]any{
					"nodes":    []any{map[string]any{"name": "screen:home"}, map[string]any{"name": "re-evaluate"}},
					"pageInfo": map[string]any{"hasNextPage": false},
				},
				"comments": map[string]any{
					"nodes": []any{map[string]any{
						"body": "[pipeline:v1:ci-red] attempt=1", "createdAt": "2026-01-02T00:00:00Z",
						"user": nil, "botActor": map[string]any{"id": "bot_1"},
					}},
					"pageInfo": map[string]any{"hasNextPage": false},
				},
				"history": map[string]any{
					"nodes": []any{
						map[string]any{ // not a state change: skipped
							"createdAt": "2026-01-03T00:00:00Z", "fromState": nil, "toState": nil,
							"actor": map[string]any{"id": "usr_a"}, "botActor": nil,
						},
						map[string]any{
							"createdAt": "2026-01-02T00:00:00Z", "fromState": map[string]any{"id": "st_0"},
							"toState": map[string]any{"id": "st_1"}, "actor": map[string]any{"id": "usr_a"}, "botActor": nil,
						},
					},
					"pageInfo": map[string]any{"hasNextPage": false},
				},
				"relations": map[string]any{
					"nodes":    []any{map[string]any{"type": "blocks", "relatedIssue": map[string]any{"id": "i9"}}},
					"pageInfo": map[string]any{"hasNextPage": false},
				},
				"inverseRelations": map[string]any{
					"nodes":    []any{map[string]any{"type": "blocks", "issue": map[string]any{"id": "i7"}}},
					"pageInfo": map[string]any{"hasNextPage": false},
				},
			})
			return issuesPage([]map[string]any{rich}, true, "cur_1"), nil
		}
		if vars["after"] != "cur_1" {
			t.Errorf("second page cursor = %v", vars["after"])
		}
		return issuesPage([]map[string]any{issueNode("i2", "PIPE-2", nil)}, false, ""), nil
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	issues, err := c.ListIssues(context.Background(), "team_1", "proj_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || calls != 2 {
		t.Fatalf("issues = %d over %d calls", len(issues), calls)
	}
	i := issues[0]
	if i.Key != "PIPE-1" || i.Milestone != "M: alpha" || len(i.Labels) != 2 {
		t.Errorf("hydration: %+v", i)
	}
	if len(i.Comments) != 1 || i.Comments[0].ActorID != "bot_1" {
		t.Errorf("comments: %+v", i.Comments)
	}
	if i.LastChange == nil || i.LastChange.FromStateID != "st_0" || i.LastChange.ActorID != "usr_a" {
		t.Errorf("last change: %+v", i.LastChange)
	}
	if !i.StateSince.Equal(i.LastChange.At) {
		t.Errorf("stateSince = %v, want the last change time", i.StateSince)
	}
	if len(i.Blocks) != 1 || i.Blocks[0] != "i9" || len(i.BlockedBy) != 1 || i.BlockedBy[0] != "i7" {
		t.Errorf("relations: blocks=%v blockedBy=%v", i.Blocks, i.BlockedBy)
	}
}

func TestListIssuesRefusesNestedOverflow(t *testing.T) {
	srv := fakeLinear(t, func(string, map[string]any) (any, []gqlError) {
		n := issueNode("i1", "PIPE-1", map[string]any{
			"comments": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": true}},
		})
		return issuesPage([]map[string]any{n}, false, ""), nil
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	_, err := c.ListIssues(context.Background(), "team_1", "proj_1")
	if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
		t.Errorf("want nested-overflow refusal, got %v", err)
	}
}

func TestIssueWritesSendTheRightMutations(t *testing.T) {
	var seen []string
	srv := fakeLinear(t, func(query string, vars map[string]any) (any, []gqlError) {
		switch {
		case strings.Contains(query, "issueUpdate"):
			input := vars["input"].(map[string]any)
			for k := range input {
				seen = append(seen, k)
			}
			return map[string]any{"issueUpdate": map[string]any{"success": true}}, nil
		case strings.Contains(query, "commentCreate"):
			seen = append(seen, "comment")
			return map[string]any{"commentCreate": map[string]any{"success": true}}, nil
		case strings.Contains(query, "issueLabels"):
			return map[string]any{"issueLabels": map[string]any{
				"nodes":    []any{map[string]any{"id": "l1", "name": "re-evaluate"}},
				"pageInfo": map[string]any{"hasNextPage": false},
			}}, nil
		default:
			t.Errorf("unexpected query: %s", query)
			return nil, nil
		}
	})
	defer srv.Close()

	ctx := context.Background()
	c := New("lin_api_test", WithEndpoint(srv.URL))
	if err := c.UpdateIssueState(ctx, "i1", "st_2"); err != nil {
		t.Fatal(err)
	}
	if err := c.CommentOnIssue(ctx, "i1", "[pipeline:v1:revert] rule=sign-off"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveIssueLabel(ctx, "team_1", "i1", "re-evaluate"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddIssueLabel(ctx, "team_1", "i1", "re-evaluate"); err != nil {
		t.Fatal(err)
	}
	want := []string{"stateId", "comment", "removedLabelIds", "addedLabelIds"}
	if len(seen) != len(want) {
		t.Fatalf("mutations = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("mutation %d = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestCreateIssueSendsMilestoneAndLabels(t *testing.T) {
	srv := fakeLinear(t, func(query string, vars map[string]any) (any, []gqlError) {
		switch {
		case strings.Contains(query, "issueCreate"):
			input := vars["input"].(map[string]any)
			if input["projectMilestoneId"] != "ms_1" || input["stateId"] != "st_todo" {
				t.Errorf("input = %v", input)
			}
			ids := input["labelIds"].([]any)
			if len(ids) != 1 || ids[0] != "l_boundary" {
				t.Errorf("labelIds = %v", ids)
			}
			return map[string]any{"issueCreate": map[string]any{
				"success": true,
				"issue": map[string]any{
					"id": "i_b", "identifier": "PIPE-99", "title": input["title"],
					"createdAt": "2026-01-05T00:00:00Z", "state": map[string]any{"id": "st_todo"},
				},
			}}, nil
		case strings.Contains(query, "issueLabels"):
			return map[string]any{"issueLabels": map[string]any{
				"nodes":    []any{map[string]any{"id": "l_boundary", "name": "milestone-boundary"}},
				"pageInfo": map[string]any{"hasNextPage": false},
			}}, nil
		default:
			t.Errorf("unexpected query: %s", query)
			return nil, nil
		}
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	i, err := c.CreateIssue(context.Background(), tracker.NewIssue{
		TeamID: "team_1", ProjectID: "proj_1", MilestoneID: "ms_1",
		Title: "Milestone boundary — M: alpha", StateID: "st_todo",
		Labels: []string{"milestone-boundary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if i.Key != "PIPE-99" {
		t.Errorf("issue = %+v", i)
	}
}

// StateSince decides whether a ticket looks stuck, and getting it wrong
// in the old direction escalated a healthy ticket to Blocked. Linear can
// report a new state before its history entry is readable, so a ticket
// moved twice in quick succession comes back as state=designing with the
// newest history entry still ->todo. Taking only the newest entry then
// found no match and fell back to the issue's creation time, which made
// a just-moved ticket look hours old.
func TestStateSinceSurvivesHistoryLaggingTheState(t *testing.T) {
	created := time.Date(2026, 8, 12, 16, 50, 0, 0, time.UTC)
	toDesigning := time.Date(2026, 8, 12, 19, 10, 0, 0, time.UTC)
	toTodo := time.Date(2026, 8, 12, 19, 11, 0, 0, time.UTC)

	issue := func(history []map[string]any) map[string]any {
		return map[string]any{
			"id": "i1", "identifier": "ORC-1", "title": "t", "description": "",
			"priority": 0, "createdAt": created,
			"state":            map[string]any{"id": "s_designing"},
			"labels":           conn(nil),
			"comments":         conn(nil),
			"history":          conn(history),
			"relations":        conn(nil),
			"inverseRelations": conn(nil),
		}
	}

	t.Run("history's newest entry is for a state the issue has already left", func(t *testing.T) {
		// The ->designing entry is present but not newest. Before the fix
		// this fell through to createdAt.
		got := oneIssue(t, issue([]map[string]any{
			{"createdAt": toDesigning, "fromState": map[string]any{"id": "s_todo"}, "toState": map[string]any{"id": "s_designing"}},
			{"createdAt": toTodo, "fromState": map[string]any{"id": "s_blocked"}, "toState": map[string]any{"id": "s_todo"}},
		}))
		if !got.StateSince.Equal(toDesigning) {
			t.Errorf("StateSince = %v, want the entry into the current state (%v)", got.StateSince, toDesigning)
		}
	})

	t.Run("no entry for the current state at all", func(t *testing.T) {
		// The move is not in our view yet. Creation time would claim the
		// ticket had sat here for hours; the newest change we can see is
		// a floor, and errs toward waiting rather than escalating.
		got := oneIssue(t, issue([]map[string]any{
			{"createdAt": toTodo, "fromState": map[string]any{"id": "s_blocked"}, "toState": map[string]any{"id": "s_todo"}},
		}))
		if !got.StateSince.Equal(toTodo) {
			t.Errorf("StateSince = %v, want the newest visible change (%v), not the creation time", got.StateSince, toTodo)
		}
	})

	t.Run("no state history at all keeps the creation time", func(t *testing.T) {
		got := oneIssue(t, issue(nil))
		if !got.StateSince.Equal(created) {
			t.Errorf("StateSince = %v, want createdAt (%v)", got.StateSince, created)
		}
	})
}

func conn(nodes []map[string]any) map[string]any {
	if nodes == nil {
		nodes = []map[string]any{}
	}
	return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
}

func oneIssue(t *testing.T, node map[string]any) tracker.Issue {
	t.Helper()
	srv := fakeLinear(t, func(string, map[string]any) (any, []gqlError) {
		return map[string]any{"issues": map[string]any{
			"nodes":    []map[string]any{node},
			"pageInfo": map[string]any{"hasNextPage": false},
		}}, nil
	})
	defer srv.Close()
	issues, err := New("lin_api_test", WithEndpoint(srv.URL)).
		ListIssues(context.Background(), "team_1", "proj_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues", len(issues))
	}
	return issues[0]
}

// Linear returns connections newest first. The port promises oldest
// first (tracker.Issue.Comments), and five readers already depended on
// that promise before anything enforced it — "the newest merged marker"
// returned the oldest, the design prompt printed a reversed history
// under a header claiming otherwise, and the rehearsal reverted commits
// in landing order rather than against it.
func TestCommentsComeBackOldestFirst(t *testing.T) {
	srv := fakeLinear(t, func(query string, vars map[string]any) (any, []gqlError) {
		return issuesPage([]map[string]any{issueNode("i1", "PIPE-1", map[string]any{
			"comments": map[string]any{
				// As Linear sends them: newest first.
				"nodes": []any{
					map[string]any{"body": "third", "createdAt": "2026-01-03T00:00:00Z", "user": nil, "botActor": nil},
					map[string]any{"body": "second", "createdAt": "2026-01-02T00:00:00Z", "user": nil, "botActor": nil},
					map[string]any{"body": "first", "createdAt": "2026-01-01T00:00:00Z", "user": nil, "botActor": nil},
				},
				"pageInfo": map[string]any{"hasNextPage": false},
			},
		})}, false, ""), nil
	})
	issues, err := New("lin_api_test", WithEndpoint(srv.URL)).ListIssues(context.Background(), "team", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || len(issues[0].Comments) != 3 {
		t.Fatalf("got %d issues", len(issues))
	}
	var got []string
	for _, c := range issues[0].Comments {
		got = append(got, c.Body)
	}
	if strings.Join(got, ",") != "first,second,third" {
		t.Errorf("comment order = %v, want oldest first", got)
	}
	// And the property the readers actually use: the last element is the
	// newest, which is what a backwards walk for "the newest marker"
	// depends on.
	if issues[0].Comments[len(issues[0].Comments)-1].Body != "third" {
		t.Error("the last comment is not the newest")
	}
}

// The relation the pipeline has always read and never written. The
// direction is the part worth pinning: ListIssues reads `relations` of
// type "blocks" as "this issue blocks relatedIssue", so the mutation has
// to put the blocker in issueId, not the other way round. Reversed, the
// queue would hold the wrong ticket and neither end would look wrong.
func TestLinkBlockingSendsTheRelationInTheDirectionListIssuesReads(t *testing.T) {
	var input map[string]any
	srv := fakeLinear(t, func(query string, vars map[string]any) (any, []gqlError) {
		if !strings.Contains(query, "issueRelationCreate") {
			t.Errorf("unexpected query: %s", query)
		}
		input = vars["input"].(map[string]any)
		return map[string]any{"issueRelationCreate": map[string]any{"success": true}}, nil
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	if err := c.LinkBlocking(context.Background(), "blocker_1", "blocked_1"); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"issueId": "blocker_1", "relatedIssueId": "blocked_1", "type": "blocks",
	} {
		if got, _ := input[k].(string); got != want {
			t.Errorf("input[%q] = %q, want %q", k, got, want)
		}
	}
}

// A mutation that reports failure without an error is the shape Linear
// uses for a refused write, and swallowing it would leave a ticket
// parked behind a blocker that does not exist.
func TestLinkBlockingReportsAnUnsuccessfulMutation(t *testing.T) {
	srv := fakeLinear(t, func(query string, vars map[string]any) (any, []gqlError) {
		return map[string]any{"issueRelationCreate": map[string]any{"success": false}}, nil
	})
	defer srv.Close()

	c := New("lin_api_test", WithEndpoint(srv.URL))
	if err := c.LinkBlocking(context.Background(), "blocker_1", "blocked_1"); err == nil {
		t.Error("a refused relation reported success")
	}
}
