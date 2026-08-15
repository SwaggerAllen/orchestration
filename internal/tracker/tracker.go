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
	// Color is cosmetic and nothing in the pipeline branches on it. It
	// is carried so setup can create states the author can read at a
	// glance, and so a test can assert it did.
	Color string
}

// NewState is a state to create. Name and Category are separate from the
// protocol state on purpose: setup also meets states that are not the
// pipeline's, and the port has no business assuming otherwise.
type NewState struct {
	Name     string
	Category protocol.Category
	// Color may be empty, which leaves the choice to the adapter.
	Color string
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
	// URL is the tracker's own link to the issue. Carried so anything
	// printed for a human can be clicked rather than reconstructed from
	// a workspace slug the pipeline would otherwise have to be told.
	URL string
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
	CreateState(ctx context.Context, teamID string, s NewState) (StateInfo, error)
	ListLabels(ctx context.Context, teamID string) ([]Label, error)
	CreateLabel(ctx context.Context, teamID, name string) (Label, error)

	ListIssues(ctx context.Context, teamID, projectID string) ([]Issue, error)
	ListMilestones(ctx context.Context, projectID string) ([]Milestone, error)
	// CreateMilestone exists for the scenario harness, which must stand a
	// rehearsal project up from nothing. The pipeline itself never creates
	// one: assigning a milestone commits the work, and that is the
	// author's call (DESIGN §10).
	CreateMilestone(ctx context.Context, projectID, name string, sortOrder float64) (Milestone, error)
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
