package linear

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/stats"
)

// statsNestedCap bounds labels and history per issue. Higher than the
// sweep's cap because history is the payload here rather than a detail:
// Catapult's ORC-217 already carries twenty-two state changes and a
// long-lived ticket can carry more. Overflow is refused rather than
// truncated — a silently short history is a ticket whose time in a state
// is simply wrong, with nothing in the output saying so.
const statsNestedCap = 250

// ListIssuesForStats fetches every issue in the project with its full
// state history, **including archived ones**.
//
// A separate query from ListIssues, deliberately, for two reasons that
// are not stylistic. ListIssues is on the sweep's critical path and its
// own doc comment records that archived issues are excluded by Linear's
// default filter — inheriting that here would under-count the oldest
// milestones and render as a downward trend with nothing about the
// output looking wrong. And this needs state *names* and the terminal
// timestamps, while needing none of the comments, relations or resolved
// roles that make the sweep's query expensive.
//
// UNVERIFIED AGAINST THE LIVE API AT THE TIME OF WRITING: the
// `includeArchived` argument is Linear's documented connection argument
// and the tracker plainly does return archived issues (ORC-23 comes back
// with its full ten-interval history), but no call from this repo has
// yet exercised this exact query. The first real run is the
// verification, which is why the collector reports the archived count it
// saw: a zero there on a project known to hold archived tickets means
// this argument is not doing what it says, and it is visible in the job
// output rather than in a chart six weeks later.
func (c *Client) ListIssuesForStats(ctx context.Context, teamID, projectID string) ([]stats.Issue, error) {
	const q = `query StatsIssues($teamId: ID!, $projectId: ID!, $first: Int!, $after: String, $nested: Int!) {
	  issues(
	    filter: {team: {id: {eq: $teamId}}, project: {id: {eq: $projectId}}},
	    first: $first, after: $after, includeArchived: true
	  ) {
	    nodes {
	      identifier title priority createdAt completedAt canceledAt archivedAt
	      state { name }
	      projectMilestone { name }
	      labels(first: $nested) { nodes { name } pageInfo { hasNextPage } }
	      history(first: $nested) {
	        nodes { createdAt fromState { name } toState { name } }
	        pageInfo { hasNextPage }
	      }
	    }
	    pageInfo { hasNextPage endCursor }
	  }
	}`

	type nameRef struct {
		Name string `json:"name"`
	}
	type page struct {
		Issues struct {
			Nodes []struct {
				Identifier  string     `json:"identifier"`
				Title       string     `json:"title"`
				Priority    int        `json:"priority"`
				CreatedAt   time.Time  `json:"createdAt"`
				CompletedAt *time.Time `json:"completedAt"`
				CanceledAt  *time.Time `json:"canceledAt"`
				ArchivedAt  *time.Time `json:"archivedAt"`
				State       *nameRef   `json:"state"`
				Milestone   *nameRef   `json:"projectMilestone"`
				Labels      struct {
					Nodes    []nameRef `json:"nodes"`
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"labels"`
				History struct {
					Nodes []struct {
						CreatedAt time.Time `json:"createdAt"`
						FromState *nameRef  `json:"fromState"`
						ToState   *nameRef  `json:"toState"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"history"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `json:"issues"`
	}

	var out []stats.Issue
	var after any
	for {
		vars := map[string]any{
			"teamId": teamID, "projectId": projectID,
			"first": pageSize, "after": after, "nested": statsNestedCap,
		}
		var data page
		if err := c.do(ctx, q, vars, &data); err != nil {
			return nil, err
		}
		for _, n := range data.Issues.Nodes {
			if n.Labels.PageInfo.HasNextPage || n.History.PageInfo.HasNextPage {
				return nil, fmt.Errorf("linear: issue %s overflows a nested page of %d — refusing to truncate a history the durations are computed from", n.Identifier, statsNestedCap)
			}
			iss := stats.Issue{
				Key: n.Identifier, Title: n.Title, Priority: n.Priority, CreatedAt: n.CreatedAt,
			}
			if n.State != nil {
				iss.CurrentState = n.State.Name
			}
			if n.Milestone != nil {
				iss.Milestone = n.Milestone.Name
			}
			for _, t := range []struct {
				src *time.Time
				dst *time.Time
			}{{n.CompletedAt, &iss.CompletedAt}, {n.CanceledAt, &iss.CanceledAt}, {n.ArchivedAt, &iss.ArchivedAt}} {
				if t.src != nil {
					*t.dst = *t.src
				}
			}
			for _, l := range n.Labels.Nodes {
				iss.Labels = append(iss.Labels, l.Name)
			}
			for _, h := range n.History.Nodes {
				if h.ToState == nil {
					continue // not a state change
				}
				tr := stats.Transition{To: h.ToState.Name, At: h.CreatedAt}
				if h.FromState != nil {
					tr.From = h.FromState.Name
				}
				iss.History = append(iss.History, tr)
			}
			// Linear's connections arrive newest first and stats.Issue
			// promises oldest first. Sorted here rather than asked for
			// in the query, matching ListIssues: the ordering is a
			// contract of the type, and a query argument is one edit
			// away from being dropped by someone tuning the GraphQL.
			//
			// A reversed history is not a visible failure. It inverts
			// every interval — each state's span becomes the one that
			// preceded it — and the numbers stay plausible.
			sort.SliceStable(iss.History, func(a, b int) bool {
				return iss.History[a].At.Before(iss.History[b].At)
			})
			out = append(out, iss)
		}
		if !data.Issues.PageInfo.HasNextPage {
			return out, nil
		}
		after = data.Issues.PageInfo.EndCursor
	}
}
