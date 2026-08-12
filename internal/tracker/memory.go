package tracker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Memory is the in-memory fake for Ring-1 and Ring-2 tests. It enforces the
// same constraints Linear does — unique names per team, a valid category on
// every state, labels existing before they attach — because a fake looser
// than the real service passes tests the dry run then fails.
type Memory struct {
	mu     sync.Mutex
	nextID int
	states map[string][]StateInfo // teamID -> states
	labels map[string][]Label     // teamID -> labels, WorkspaceScope -> workspace labels

	issues     []*Issue
	milestones map[string][]Milestone // projectID -> milestones
	issueTeam  map[string]string      // issueID -> teamID
	issueProj  map[string]string      // issueID -> projectID
	archived   map[string]bool        // issueID -> archived

	// ActorID stamps writes, as Linear stamps the API key's user. Tests
	// map it to a role in config.Actors.
	ActorID string
	// Now supplies timestamps; virtual in tests.
	Now func() time.Time
}

var _ Tracker = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{
		states:     map[string][]StateInfo{},
		labels:     map[string][]Label{},
		milestones: map[string][]Milestone{},
		issueTeam:  map[string]string{},
		issueProj:  map[string]string{},
		archived:   map[string]bool{},
		ActorID:    "memory-bot",
		Now:        time.Now,
	}
}

func (m *Memory) id(kind string) string {
	m.nextID++
	return fmt.Sprintf("%s_%d", kind, m.nextID)
}

func (m *Memory) ListStates(_ context.Context, teamID string) ([]StateInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]StateInfo, len(m.states[teamID]))
	copy(out, m.states[teamID])
	return out, nil
}

func (m *Memory) CreateState(_ context.Context, teamID, name string, category protocol.Category) (StateInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name == "" {
		return StateInfo{}, fmt.Errorf("memory tracker: state name is required")
	}
	switch category {
	case protocol.CategoryBacklog, protocol.CategoryUnstarted, protocol.CategoryStarted,
		protocol.CategoryCompleted, protocol.CategoryCanceled, protocol.CategoryTriage:
	default:
		return StateInfo{}, fmt.Errorf("memory tracker: invalid state category %q", category)
	}
	for _, s := range m.states[teamID] {
		if s.Name == name {
			return StateInfo{}, fmt.Errorf("memory tracker: state %q already exists in team %s", name, teamID)
		}
	}
	s := StateInfo{ID: m.id("state"), Name: name, Category: category}
	m.states[teamID] = append(m.states[teamID], s)
	return s, nil
}

// WorkspaceScope is the labels key for labels that belong to no team.
// Linear's workspace-level labels are usable by every team but carry no
// team id, so a team-scoped read misses them entirely — the fake models
// the two scopes so that gap is reachable from a test.
const WorkspaceScope = ""

// ListLabels returns what the team can apply: workspace labels first, then
// its own, matching what the Linear client's filter now asks for.
func (m *Memory) ListLabels(_ context.Context, teamID string) ([]Label, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.visibleLabels(teamID), nil
}

// visibleLabels is every label the team can apply — workspace-scoped plus
// its own. Every existence check goes through it so none of them can
// regress to the team-only read that hid the workspace half.
func (m *Memory) visibleLabels(teamID string) []Label {
	out := make([]Label, 0, len(m.labels[WorkspaceScope])+len(m.labels[teamID]))
	out = append(out, m.labels[WorkspaceScope]...)
	if teamID != WorkspaceScope {
		out = append(out, m.labels[teamID]...)
	}
	return out
}

func (m *Memory) CreateLabel(_ context.Context, teamID, name string) (Label, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createLabel(teamID, name)
}

// AddWorkspaceLabel seeds a workspace-scoped label. Tests use it to stand
// up the shape a real workspace has: a shared taxonomy no team owns.
func (m *Memory) AddWorkspaceLabel(name string) (Label, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createLabel(WorkspaceScope, name)
}

// createLabel enforces Linear's uniqueness: a name is taken if any scope
// the team can see already holds it, not just the scope being written to.
func (m *Memory) createLabel(scope, name string) (Label, error) {
	if name == "" {
		return Label{}, fmt.Errorf("memory tracker: label name is required")
	}
	for _, l := range m.visibleLabels(scope) {
		if l.Name == name {
			return Label{}, fmt.Errorf("memory tracker: label %q already exists", name)
		}
	}
	l := Label{ID: m.id("label"), Name: name}
	m.labels[scope] = append(m.labels[scope], l)
	return l, nil
}

func (m *Memory) CreateMilestone(_ context.Context, projectID, name string, sortOrder float64) (Milestone, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name == "" {
		return Milestone{}, fmt.Errorf("memory tracker: milestone name is required")
	}
	for _, ms := range m.milestones[projectID] {
		if ms.Name == name {
			return Milestone{}, fmt.Errorf("memory tracker: milestone %q already exists in project %s", name, projectID)
		}
	}
	ms := Milestone{ID: m.id("milestone"), Name: name, SortOrder: sortOrder}
	m.milestones[projectID] = append(m.milestones[projectID], ms)
	return ms, nil
}

// AddMilestone is a test helper: milestones are author-created in Linear,
// so the port has no write for them.
func (m *Memory) AddMilestone(projectID, name string, sortOrder float64) Milestone {
	m.mu.Lock()
	defer m.mu.Unlock()
	ms := Milestone{ID: m.id("milestone"), Name: name, SortOrder: sortOrder}
	m.milestones[projectID] = append(m.milestones[projectID], ms)
	return ms
}

func (m *Memory) ListMilestones(_ context.Context, projectID string) ([]Milestone, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Milestone, len(m.milestones[projectID]))
	copy(out, m.milestones[projectID])
	return out, nil
}

func (m *Memory) ListIssues(_ context.Context, teamID, projectID string) ([]Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Issue
	for _, i := range m.issues {
		// Archived issues vanish from listings, as in Linear — which is
		// exactly why the retro note exists (DESIGN §10).
		if m.archived[i.ID] {
			continue
		}
		if m.issueTeam[i.ID] == teamID && m.issueProj[i.ID] == projectID {
			out = append(out, cloneIssue(i))
		}
	}
	return out, nil
}

func cloneIssue(i *Issue) Issue {
	c := *i
	c.Labels = append([]string(nil), i.Labels...)
	c.Comments = append([]IssueComment(nil), i.Comments...)
	c.Blocks = append([]string(nil), i.Blocks...)
	c.BlockedBy = append([]string(nil), i.BlockedBy...)
	if i.LastChange != nil {
		lc := *i.LastChange
		c.LastChange = &lc
	}
	return c
}

func (m *Memory) CreateIssue(_ context.Context, n NewIssue) (Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n.Title == "" || n.StateID == "" {
		return Issue{}, fmt.Errorf("memory tracker: title and state are required")
	}
	haveLabels := map[string]bool{}
	for _, l := range m.visibleLabels(n.TeamID) {
		haveLabels[l.Name] = true
	}
	for _, l := range n.Labels {
		if !haveLabels[l] {
			return Issue{}, fmt.Errorf("memory tracker: label %q does not exist in team %s", l, n.TeamID)
		}
	}
	milestone := ""
	if n.MilestoneID != "" {
		for _, ms := range m.milestones[n.ProjectID] {
			if ms.ID == n.MilestoneID {
				milestone = ms.Name
			}
		}
		if milestone == "" {
			return Issue{}, fmt.Errorf("memory tracker: milestone %q not in project %s", n.MilestoneID, n.ProjectID)
		}
	}
	now := m.Now()
	id := m.id("issue")
	i := &Issue{
		ID: id, Key: fmt.Sprintf("MEM-%d", m.nextID), Title: n.Title, Description: n.Description,
		StateID: n.StateID, Labels: append([]string(nil), n.Labels...),
		Priority: 3, Milestone: milestone,
		CreatedAt: now, StateSince: now,
	}
	m.issues = append(m.issues, i)
	m.issueTeam[id] = n.TeamID
	m.issueProj[id] = n.ProjectID
	return cloneIssue(i), nil
}

func (m *Memory) find(issueID string) (*Issue, error) {
	for _, i := range m.issues {
		if i.ID == issueID {
			return i, nil
		}
	}
	return nil, fmt.Errorf("memory tracker: no issue %q", issueID)
}

func (m *Memory) UpdateIssueState(_ context.Context, issueID, stateID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.find(issueID)
	if err != nil {
		return err
	}
	teamID := m.issueTeam[issueID]
	valid := false
	for _, s := range m.states[teamID] {
		if s.ID == stateID {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("memory tracker: state %q not in team %s", stateID, teamID)
	}
	now := m.Now()
	i.LastChange = &StateChange{FromStateID: i.StateID, ToStateID: stateID, ActorID: m.ActorID, At: now}
	i.StateID = stateID
	i.StateSince = now
	return nil
}

func (m *Memory) CommentOnIssue(_ context.Context, issueID, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.find(issueID)
	if err != nil {
		return err
	}
	i.Comments = append(i.Comments, IssueComment{Body: body, ActorID: m.ActorID, CreatedAt: m.Now()})
	return nil
}

func (m *Memory) AddIssueLabel(_ context.Context, teamID, issueID, label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.find(issueID)
	if err != nil {
		return err
	}
	exists := false
	for _, l := range m.visibleLabels(teamID) {
		if l.Name == label {
			exists = true
		}
	}
	if !exists {
		return fmt.Errorf("memory tracker: label %q does not exist in team %s", label, teamID)
	}
	for _, l := range i.Labels {
		if l == label {
			return nil
		}
	}
	i.Labels = append(i.Labels, label)
	return nil
}

func (m *Memory) RemoveIssueLabel(_ context.Context, teamID, issueID, label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.find(issueID)
	if err != nil {
		return err
	}
	var keep []string
	for _, l := range i.Labels {
		if l != label {
			keep = append(keep, l)
		}
	}
	i.Labels = keep
	return nil
}

func (m *Memory) ArchiveIssue(_ context.Context, issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.find(issueID); err != nil {
		return err
	}
	m.archived[issueID] = true
	return nil
}

func (m *Memory) AssignIssue(_ context.Context, issueID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.find(issueID)
	if err != nil {
		return err
	}
	i.AssigneeID = userID
	return nil
}

func (m *Memory) UpdateIssuePriority(_ context.Context, issueID string, priority int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.find(issueID)
	if err != nil {
		return err
	}
	if priority < 0 || priority > 4 {
		return fmt.Errorf("memory tracker: priority %d out of range", priority)
	}
	i.Priority = priority
	return nil
}

// Archived reports whether an issue is archived (test helper).
func (m *Memory) Archived(issueID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.archived[issueID]
}

// Mutate is a test helper: reach into a stored issue to script conditions
// the port deliberately has no writes for (priority, relations, history).
func (m *Memory) Mutate(issueID string, f func(*Issue)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.find(issueID)
	if err != nil {
		return err
	}
	f(i)
	return nil
}
