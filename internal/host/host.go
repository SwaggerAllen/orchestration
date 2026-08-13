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
	// RecordDeployment creates a successful GitHub Deployment for a
	// commit. It exists only for projects whose deploy provider is
	// "github" — the dummy's stand-in for a real platform (PLAN M4) —
	// and reconcile calls it straight after merging.
	//
	// It used to be a project workflow triggered by the push to main.
	// That trigger cannot fire for the pipeline: GitHub does not start a
	// workflow run from an event created with GITHUB_TOKEN, and
	// reconcile merges with exactly that, so no deployment was ever
	// recorded for an agent merge and every ticket sat in Merged until
	// the deploy timeout moved it to Blocked.
	RecordDeployment(ctx context.Context, sha, environment string) error
	// PutFileIfAbsent commits one file to the default branch unless it
	// already exists, reporting whether it was created. The boundary's
	// retro note uses it: re-run safety demands the existence check
	// (DESIGN §10), and the boundary never opens a PR — machinery, not
	// work.
	PutFileIfAbsent(ctx context.Context, path, content, message string) (bool, error)
}
