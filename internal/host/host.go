// Package host defines the port to the code host (GitHub). The control
// plane reads runs and PRs through it to populate snapshots, and the agent
// harness writes PRs through it. The sim uses real temporary git
// repositories for repo state — git is local and free — so only the hosted
// surface (API calls) is faked.
package host

import (
	"context"
	"time"
)

// AgentRun is one agent workflow run, correlated to a ticket by the
// run-name convention "pipeline: <kind> <ticket-key>" — the stub workflows
// set run-name exactly so this correlation exists (PLAN M3).
type AgentRun struct {
	ID        string
	Kind      string // "design" | "dev" | "reconcile" | "boundary"
	TicketKey string
	Live      bool
	EndedAt   time.Time
	URL       string
}

// PR is one open pull request.
type PR struct {
	Number  int
	Branch  string
	HeadSHA string
	Draft   bool
	URL     string
}

// CheckStatus aggregates a commit's check runs.
type CheckStatus string

const (
	ChecksNone    CheckStatus = ""
	ChecksPending CheckStatus = "pending"
	ChecksGreen   CheckStatus = "green"
	ChecksRed     CheckStatus = "red"
)

// Checks is the aggregate verdict for one head SHA. RunURL points at a
// failing run when red — it goes into the CI failure marker (DESIGN §12).
type Checks struct {
	Status CheckStatus
	RunURL string
}

// Host is the port.
type Host interface {
	DispatchWorkflow(ctx context.Context, workflowFile string, inputs map[string]string) error
	ListAgentRuns(ctx context.Context) ([]AgentRun, error)
	ListOpenPRs(ctx context.Context) ([]PR, error)
	ChecksFor(ctx context.Context, headSHA string) (Checks, error)
	CreatePR(ctx context.Context, branch, title, body string, draft bool) (PR, error)
	MarkPRReady(ctx context.Context, number int) error
	// MergePR squash-merges and returns the merge commit SHA — the value
	// the post-deploy check compares against the platform (DESIGN §13).
	MergePR(ctx context.Context, number int) (string, error)
	// IsAncestor reports whether ancestor is reachable from descendant:
	// the real meaning of the design's `>=` (DESIGN §13).
	IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error)
}
