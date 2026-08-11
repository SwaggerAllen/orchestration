// Package tracker defines the port to the issue tracker. The pure core and
// the setup command speak only this interface; Linear lives behind it in the
// linear subpackage, and an in-memory fake stands in for Ring-1 and Ring-2
// tests. The port grows with the milestones: M0 added provisioning (states
// and labels), M2 adds the issue surface the snapshot builder and the
// action executor need.
package tracker

import (
	"context"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// StateInfo is one workflow state as the tracker has it.
type StateInfo struct {
	ID       string
	Name     string
	Category protocol.Category
}

// Label is one team label.
type Label struct {
	ID   string
	Name string
}

// Milestone is one project milestone. SortOrder is the tracker's ordering,
// which is what "the next milestone" means (DESIGN §10).
type Milestone struct {
	ID        string
	Name      string
	SortOrder float64
}

// IssueComment is one comment, with the actor's tracker identity — the
// snapshot builder resolves identities to roles via config, so raw ids
// stop at the adapter boundary.
type IssueComment struct {
	Body      string
	ActorID   string
	CreatedAt time.Time
}

// StateChange is one state transition from the issue's history.
type StateChange struct {
	FromStateID string
	ToStateID   string
	ActorID     string
	At          time.Time
}

// Issue is one issue, hydrated: the tracker adapter fetches comments,
// history and relations alongside the issue so a snapshot is a bounded
// number of API calls rather than one per ticket.
type Issue struct {
	ID          string
	Key         string
	Title       string
	Description string
	StateID     string
	Labels      []string
	Priority    int
	AssigneeID  string // "" = unassigned
	Milestone   string // milestone name; "" if none
	CreatedAt   time.Time
	// StateSince is when the issue entered its current state, from
	// history; falls back to CreatedAt when history has no state change.
	StateSince time.Time
	// LastChange is the most recent state transition, if any.
	LastChange *StateChange
	Comments   []IssueComment
	// Blocks and BlockedBy are issue IDs from blocking relations.
	Blocks    []string
	BlockedBy []string
}

// NewIssue is the creation surface — exactly what the boundary ticket
// needs, nothing speculative (DESIGN §10).
type NewIssue struct {
	TeamID      string
	ProjectID   string
	MilestoneID string
	Title       string
	Description string
	StateID     string
	Labels      []string
}

// Tracker is the port. Reads are scoped by team and project because the
// queue is a state scoped by a project (DESIGN §2) and nothing may read
// wider than its scope.
type Tracker interface {
	ListStates(ctx context.Context, teamID string) ([]StateInfo, error)
	CreateState(ctx context.Context, teamID, name string, category protocol.Category) (StateInfo, error)
	ListLabels(ctx context.Context, teamID string) ([]Label, error)
	CreateLabel(ctx context.Context, teamID, name string) (Label, error)

	ListIssues(ctx context.Context, teamID, projectID string) ([]Issue, error)
	ListMilestones(ctx context.Context, projectID string) ([]Milestone, error)
	CreateIssue(ctx context.Context, n NewIssue) (Issue, error)
	UpdateIssueState(ctx context.Context, issueID, stateID string) error
	CommentOnIssue(ctx context.Context, issueID, body string) error
	AddIssueLabel(ctx context.Context, teamID, issueID, label string) error
	RemoveIssueLabel(ctx context.Context, teamID, issueID, label string) error
	// ArchiveIssue is the boundary's archive pass (DESIGN §10). Naturally
	// idempotent: archiving the archived is a no-op.
	ArchiveIssue(ctx context.Context, issueID string) error
	// UpdateIssuePriority is the grooming re-rank (DESIGN §8, §10).
	UpdateIssuePriority(ctx context.Context, issueID string, priority int) error
	// AssignIssue sets the assignee; "" unassigns. Assignment mirrors who
	// has the ball (DESIGN §3).
	AssignIssue(ctx context.Context, issueID, userID string) error
}
