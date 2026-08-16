package linear

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// issuePage is Linear's page cap for the top-level issue list. Nested
// connections (comments, history, relations) are capped per issue and a
// full nested page is an error rather than silent truncation — a miscount
// there corrupts escalation counting (DESIGN §12).
const nestedCap = 100

type actorRef struct {
	ID string `json:"id"`
}

func actorID(user, bot *actorRef) string {
	if user != nil {
		return user.ID
	}
	if bot != nil {
		return bot.ID
	}
	return ""
}

// ListIssues fetches the project's issues fully hydrated, paginating the
// top level. Archived issues are excluded by Linear's default filter,
// which is what the retro note exists to compensate for (DESIGN §10).
func (c *Client) ListIssues(ctx context.Context, teamID, projectID string) ([]tracker.Issue, error) {
	const q = `query Issues($teamId: ID!, $projectId: ID!, $first: Int!, $after: String, $nested: Int!) {
	  issues(
	    filter: {team: {id: {eq: $teamId}}, project: {id: {eq: $projectId}}},
	    first: $first, after: $after
	  ) {
	    nodes {
	      id identifier title description priority createdAt url
	      state { id }
	      assignee { id }
	      projectMilestone { name }
	      labels(first: $nested) { nodes { name } pageInfo { hasNextPage } }
	      comments(first: $nested) {
	        nodes { body createdAt user { id } botActor { id } }
	        pageInfo { hasNextPage }
	      }
	      history(first: $nested) {
	        nodes { createdAt fromState { id } toState { id } actor { id } botActor { id } }
	        pageInfo { hasNextPage }
	      }
	      relations(first: $nested) {
	        nodes { type relatedIssue { id } }
	        pageInfo { hasNextPage }
	      }
	      inverseRelations(first: $nested) {
	        nodes { type issue { id } }
	        pageInfo { hasNextPage }
	      }
	    }
	    pageInfo { hasNextPage endCursor }
	  }
	}`

	type page struct {
		Issues struct {
			Nodes []struct {
				ID          string    `json:"id"`
				Identifier  string    `json:"identifier"`
				Title       string    `json:"title"`
				Description string    `json:"description"`
				Priority    int       `json:"priority"`
				CreatedAt   time.Time `json:"createdAt"`
				URL         string    `json:"url"`
				State       actorRef  `json:"state"`
				Assignee    *actorRef `json:"assignee"`
				Milestone   *struct {
					Name string `json:"name"`
				} `json:"projectMilestone"`
				Labels struct {
					Nodes []struct {
						Name string `json:"name"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"labels"`
				Comments struct {
					Nodes []struct {
						Body      string    `json:"body"`
						CreatedAt time.Time `json:"createdAt"`
						User      *actorRef `json:"user"`
						BotActor  *actorRef `json:"botActor"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"comments"`
				History struct {
					Nodes []struct {
						CreatedAt time.Time `json:"createdAt"`
						FromState *actorRef `json:"fromState"`
						ToState   *actorRef `json:"toState"`
						Actor     *actorRef `json:"actor"`
						BotActor  *actorRef `json:"botActor"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"history"`
				Relations struct {
					Nodes []struct {
						Type         string   `json:"type"`
						RelatedIssue actorRef `json:"relatedIssue"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"relations"`
				InverseRelations struct {
					Nodes []struct {
						Type  string   `json:"type"`
						Issue actorRef `json:"issue"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"inverseRelations"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `json:"issues"`
	}

	var out []tracker.Issue
	var after any
	for {
		vars := map[string]any{
			"teamId": teamID, "projectId": projectID,
			"first": pageSize, "after": after, "nested": nestedCap,
		}
		var data page
		if err := c.do(ctx, q, vars, &data); err != nil {
			return nil, err
		}
		for _, n := range data.Issues.Nodes {
			if n.Labels.PageInfo.HasNextPage || n.Comments.PageInfo.HasNextPage ||
				n.History.PageInfo.HasNextPage || n.Relations.PageInfo.HasNextPage ||
				n.InverseRelations.PageInfo.HasNextPage {
				return nil, fmt.Errorf("linear: issue %s overflows a nested page of %d — refusing to truncate what escalation counting reads", n.Identifier, nestedCap)
			}
			issue := tracker.Issue{
				ID: n.ID, Key: n.Identifier, Title: n.Title, Description: n.Description,
				StateID: n.State.ID, Priority: n.Priority, CreatedAt: n.CreatedAt,
				StateSince: n.CreatedAt, URL: n.URL,
			}
			if n.Assignee != nil {
				issue.AssigneeID = n.Assignee.ID
			}
			if n.Milestone != nil {
				issue.Milestone = n.Milestone.Name
			}
			for _, l := range n.Labels.Nodes {
				issue.Labels = append(issue.Labels, l.Name)
			}
			for _, cm := range n.Comments.Nodes {
				issue.Comments = append(issue.Comments, tracker.IssueComment{
					Body: cm.Body, ActorID: actorID(cm.User, cm.BotActor), CreatedAt: cm.CreatedAt,
				})
			}
			// Linear's connections arrive newest first; the port promises
			// oldest first (tracker.Issue.Comments). Sorted here rather
			// than asked for in the query because the ordering is a
			// contract of the port, and a query argument is one edit away
			// from being dropped by someone tuning the GraphQL.
			sort.SliceStable(issue.Comments, func(a, b int) bool {
				return issue.Comments[a].CreatedAt.Before(issue.Comments[b].CreatedAt)
			})
			var newestEntry, newestChange time.Time
			for _, h := range n.History.Nodes {
				if h.ToState == nil {
					continue // not a state change
				}
				sc := tracker.StateChange{
					ToStateID: h.ToState.ID, ActorID: actorID(h.Actor, h.BotActor), At: h.CreatedAt,
				}
				if h.FromState != nil {
					sc.FromStateID = h.FromState.ID
				}
				if issue.LastChange == nil || sc.At.After(issue.LastChange.At) {
					lc := sc
					issue.LastChange = &lc
				}
				// Track entry into the *current* state separately. The
				// newest change overall is not always it: Linear can
				// report a new state before its history entry is
				// readable, so a ticket moved twice in quick succession
				// shows state=B with the newest entry still ->A.
				if sc.ToStateID == issue.StateID && sc.At.After(newestEntry) {
					newestEntry = sc.At
				}
				if sc.At.After(newestChange) {
					newestChange = sc.At
				}
			}
			switch {
			case !newestEntry.IsZero():
				issue.StateSince = newestEntry
			case !newestChange.IsZero():
				// The state moved after the history we can see. Its entry
				// is at least as recent as the newest change we do see —
				// far closer to the truth than the creation time, and in
				// the safe direction: too-old here escalates a healthy
				// ticket to Blocked, while too-recent only waits a beat.
				issue.StateSince = newestChange
			}
			for _, r := range n.Relations.Nodes {
				if r.Type == "blocks" {
					issue.Blocks = append(issue.Blocks, r.RelatedIssue.ID)
				}
			}
			for _, r := range n.InverseRelations.Nodes {
				if r.Type == "blocks" {
					issue.BlockedBy = append(issue.BlockedBy, r.Issue.ID)
				}
			}
			out = append(out, issue)
		}
		if !data.Issues.PageInfo.HasNextPage {
			return out, nil
		}
		after = data.Issues.PageInfo.EndCursor
	}
}

func (c *Client) ListMilestones(ctx context.Context, projectID string) ([]tracker.Milestone, error) {
	const q = `query Milestones($projectId: String!, $first: Int!) {
	  project(id: $projectId) {
	    projectMilestones(first: $first) {
	      nodes { id name sortOrder }
	      pageInfo { hasNextPage }
	    }
	  }
	}`
	var data struct {
		Project struct {
			ProjectMilestones struct {
				Nodes []struct {
					ID        string  `json:"id"`
					Name      string  `json:"name"`
					SortOrder float64 `json:"sortOrder"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool `json:"hasNextPage"`
				} `json:"pageInfo"`
			} `json:"projectMilestones"`
		} `json:"project"`
	}
	if err := c.do(ctx, q, map[string]any{"projectId": projectID, "first": pageSize}, &data); err != nil {
		return nil, err
	}
	if data.Project.ProjectMilestones.PageInfo.HasNextPage {
		return nil, fmt.Errorf("linear: project %s has over %d milestones, which the pipeline does not expect", projectID, pageSize)
	}
	out := make([]tracker.Milestone, 0, len(data.Project.ProjectMilestones.Nodes))
	for _, n := range data.Project.ProjectMilestones.Nodes {
		out = append(out, tracker.Milestone{ID: n.ID, Name: n.Name, SortOrder: n.SortOrder})
	}
	return out, nil
}

// CreateMilestone is used only by the scenario harness standing a
// rehearsal project up from nothing (PLAN §2). The pipeline proper never
// calls it: assigning work to a milestone commits it, and that is the
// author's decision (DESIGN §10).
func (c *Client) CreateMilestone(ctx context.Context, projectID, name string, sortOrder float64) (tracker.Milestone, error) {
	const q = `mutation CreateMilestone($input: ProjectMilestoneCreateInput!) {
	  projectMilestoneCreate(input: $input) {
	    success
	    projectMilestone { id name sortOrder }
	  }
	}`
	var data struct {
		ProjectMilestoneCreate struct {
			Success          bool `json:"success"`
			ProjectMilestone struct {
				ID        string  `json:"id"`
				Name      string  `json:"name"`
				SortOrder float64 `json:"sortOrder"`
			} `json:"projectMilestone"`
		} `json:"projectMilestoneCreate"`
	}
	vars := map[string]any{"input": map[string]any{
		"projectId": projectID,
		"name":      name,
		"sortOrder": sortOrder,
	}}
	if err := c.do(ctx, q, vars, &data); err != nil {
		return tracker.Milestone{}, err
	}
	if !data.ProjectMilestoneCreate.Success {
		return tracker.Milestone{}, fmt.Errorf("linear: projectMilestoneCreate(%q) reported failure", name)
	}
	m := data.ProjectMilestoneCreate.ProjectMilestone
	return tracker.Milestone{ID: m.ID, Name: m.Name, SortOrder: m.SortOrder}, nil
}

func (c *Client) CreateIssue(ctx context.Context, n tracker.NewIssue) (tracker.Issue, error) {
	labelIDs, err := c.labelIDs(ctx, n.TeamID, n.Labels)
	if err != nil {
		return tracker.Issue{}, err
	}
	const q = `mutation CreateIssue($input: IssueCreateInput!) {
	  issueCreate(input: $input) {
	    success
	    issue { id identifier title createdAt state { id } }
	  }
	}`
	input := map[string]any{
		"teamId":      n.TeamID,
		"projectId":   n.ProjectID,
		"title":       n.Title,
		"description": n.Description,
		"stateId":     n.StateID,
		"labelIds":    labelIDs,
	}
	if n.MilestoneID != "" {
		input["projectMilestoneId"] = n.MilestoneID
	}
	var data struct {
		IssueCreate struct {
			Success bool `json:"success"`
			Issue   struct {
				ID         string    `json:"id"`
				Identifier string    `json:"identifier"`
				Title      string    `json:"title"`
				CreatedAt  time.Time `json:"createdAt"`
				State      actorRef  `json:"state"`
			} `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := c.do(ctx, q, map[string]any{"input": input}, &data); err != nil {
		return tracker.Issue{}, err
	}
	if !data.IssueCreate.Success {
		return tracker.Issue{}, fmt.Errorf("linear: issueCreate(%q) reported failure", n.Title)
	}
	i := data.IssueCreate.Issue
	return tracker.Issue{
		ID: i.ID, Key: i.Identifier, Title: i.Title,
		StateID: i.State.ID, CreatedAt: i.CreatedAt, StateSince: i.CreatedAt,
		Labels: append([]string(nil), n.Labels...),
	}, nil
}

func (c *Client) UpdateIssueState(ctx context.Context, issueID, stateID string) error {
	return c.issueUpdate(ctx, issueID, map[string]any{"stateId": stateID})
}

func (c *Client) AddIssueLabel(ctx context.Context, teamID, issueID, label string) error {
	ids, err := c.labelIDs(ctx, teamID, []string{label})
	if err != nil {
		return err
	}
	return c.issueUpdate(ctx, issueID, map[string]any{"addedLabelIds": ids})
}

func (c *Client) RemoveIssueLabel(ctx context.Context, teamID, issueID, label string) error {
	ids, err := c.labelIDs(ctx, teamID, []string{label})
	if err != nil {
		return err
	}
	return c.issueUpdate(ctx, issueID, map[string]any{"removedLabelIds": ids})
}

func (c *Client) issueUpdate(ctx context.Context, issueID string, input map[string]any) error {
	const q = `mutation UpdateIssue($id: String!, $input: IssueUpdateInput!) {
	  issueUpdate(id: $id, input: $input) { success }
	}`
	var data struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := c.do(ctx, q, map[string]any{"id": issueID, "input": input}, &data); err != nil {
		return err
	}
	if !data.IssueUpdate.Success {
		return fmt.Errorf("linear: issueUpdate(%s) reported failure", issueID)
	}
	return nil
}

func (c *Client) ArchiveIssue(ctx context.Context, issueID string) error {
	const q = `mutation Archive($id: String!) {
	  issueArchive(id: $id) { success }
	}`
	var data struct {
		IssueArchive struct {
			Success bool `json:"success"`
		} `json:"issueArchive"`
	}
	if err := c.do(ctx, q, map[string]any{"id": issueID}, &data); err != nil {
		return err
	}
	if !data.IssueArchive.Success {
		return fmt.Errorf("linear: issueArchive(%s) reported failure", issueID)
	}
	return nil
}

func (c *Client) UpdateIssuePriority(ctx context.Context, issueID string, priority int) error {
	return c.issueUpdate(ctx, issueID, map[string]any{"priority": priority})
}

// AssignIssue sets or clears the assignee. Linear unassigns on an
// explicit null, which is why the value is nil rather than omitted.
func (c *Client) AssignIssue(ctx context.Context, issueID, userID string) error {
	var v any
	if userID != "" {
		v = userID
	}
	return c.issueUpdate(ctx, issueID, map[string]any{"assigneeId": v})
}

func (c *Client) CommentOnIssue(ctx context.Context, issueID, body string) error {
	const q = `mutation CreateComment($input: CommentCreateInput!) {
	  commentCreate(input: $input) { success }
	}`
	var data struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	if err := c.do(ctx, q, map[string]any{"input": map[string]any{"issueId": issueID, "body": body}}, &data); err != nil {
		return err
	}
	if !data.CommentCreate.Success {
		return fmt.Errorf("linear: commentCreate(%s) reported failure", issueID)
	}
	return nil
}

// labelIDs resolves label names to ids within a team, erroring on any miss
// so a typo'd label fails the action rather than silently narrowing it.
func (c *Client) labelIDs(ctx context.Context, teamID string, names []string) ([]string, error) {
	labels, err := c.ListLabels(ctx, teamID)
	if err != nil {
		return nil, err
	}
	byName := map[string]string{}
	for _, l := range labels {
		byName[l.Name] = l.ID
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		id, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("linear: no label %q in team %s", n, teamID)
		}
		out = append(out, id)
	}
	return out, nil
}

// Identity is what `pipeline ids` prints to help fill a config.
type Identity struct {
	ViewerID   string
	ViewerName string
	Teams      []IdentityTeam
}

type IdentityTeam struct {
	ID       string
	Key      string
	Name     string
	Projects []IdentityProject
}

type IdentityProject struct {
	ID   string
	Name string
}

// Whoami fetches the API key's own identity plus visible teams and
// projects — everything a config's ids section needs, in one call.
func (c *Client) Whoami(ctx context.Context) (*Identity, error) {
	const q = `query Whoami($first: Int!) {
	  viewer { id name }
	  teams(first: $first) {
	    nodes {
	      id key name
	      projects(first: $first) { nodes { id name } }
	    }
	  }
	}`
	var data struct {
		Viewer struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"viewer"`
		Teams struct {
			Nodes []struct {
				ID       string `json:"id"`
				Key      string `json:"key"`
				Name     string `json:"name"`
				Projects struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"projects"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := c.do(ctx, q, map[string]any{"first": 50}, &data); err != nil {
		return nil, err
	}
	id := &Identity{ViewerID: data.Viewer.ID, ViewerName: data.Viewer.Name}
	for _, t := range data.Teams.Nodes {
		team := IdentityTeam{ID: t.ID, Key: t.Key, Name: t.Name}
		for _, p := range t.Projects.Nodes {
			team.Projects = append(team.Projects, IdentityProject{ID: p.ID, Name: p.Name})
		}
		id.Teams = append(id.Teams, team)
	}
	return id, nil
}
