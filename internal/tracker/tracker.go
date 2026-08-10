// Package tracker defines the port to the issue tracker. The pure core and
// the setup command speak only this interface; Linear lives behind it in the
// linear subpackage, and an in-memory fake stands in for Ring-1 and Ring-2
// tests. The port grows with the milestones — M0 needs only what setup
// touches (states and labels), and speculative surface here would just be
// untested surface.
package tracker

import (
	"context"

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

// Tracker is the port. All methods are scoped by team id because the queue
// is a state scoped by a project (DESIGN §2) and nothing may read wider than
// its scope.
type Tracker interface {
	ListStates(ctx context.Context, teamID string) ([]StateInfo, error)
	CreateState(ctx context.Context, teamID, name string, category protocol.Category) (StateInfo, error)
	ListLabels(ctx context.Context, teamID string) ([]Label, error)
	CreateLabel(ctx context.Context, teamID, name string) (Label, error)
}
