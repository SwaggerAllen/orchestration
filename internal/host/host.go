// Package host defines the port to the code host (GitHub). The control
// plane reads runs and PRs through it to populate snapshots, and the agent
// harness writes PRs through it. The sim uses real temporary git
// repositories for repo state — git is local and free — so only the hosted
// surface (API calls) is faked.
package host

import (
	"context"
	"errors"
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

// MergeState is whether a PR's branch can land on its base.
//
// Tri-state, and the third value is the whole reason this is not a bool.
// GitHub computes mergeability in the background and answers `null`
// until it has — so "no" and "not yet" arrive as the same absence, and a
// caller that reads absence as a conflict will bounce a healthy ticket
// the first time it asks about a freshly pushed branch.
type MergeState string

const (
	// MergeUnknown: GitHub has not computed it yet. Not a conflict, and
	// must never be acted on as one.
	MergeUnknown MergeState = ""
	MergeClean   MergeState = "clean"
	// MergeConflicted: the branch conflicts with its base.
	MergeConflicted MergeState = "conflicted"
)

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
	// FailedJobs names the checks that are not green, so the failure
	// comment can say what broke. DESIGN §12 always described the comment
	// as "naming the failing jobs and linking the run"; for a long time it
	// only linked, and the link is the half an agent cannot follow.
	FailedJobs []string
}

// JobLog is one failing CI job with the tail of its output. The dev
// agent's rework scope is the failure comment, and a comment that says
// "fix what the linked run reports" is only a scope to a reader who can
// open the run. The agent cannot: it holds no GitHub credential by
// design, and reading CI is not a reason to give it one. So the harness
// reads and the agent is handed the text — the same division as the
// non-asks document (DESIGN §4) and the ticket body itself.
type JobLog struct {
	Name string
	URL  string
	// Log is a bounded tail, not the whole job. Whole logs are mostly
	// setup and are long enough to crowd out the argument they are
	// evidence for.
	Log string
}

// ErrNotMergeable is returned by MergePR when the branch cannot merge
// into its base — a conflict with something that landed while this
// ticket was in flight.
//
// A sentinel rather than a string match, because the caller acts on it:
// a conflict is not a failed run, it is a stale branch, and the ticket
// goes back to the dev agent to merge and resolve. Treating it as any
// other merge error sent the ticket to Blocked, where only the author
// could move it, for the one problem in this pipeline an agent is
// unambiguously equipped to fix.
var ErrNotMergeable = errors.New("pull request is not mergeable")

// Host is the port.
type Host interface {
	DispatchWorkflow(ctx context.Context, workflowFile string, inputs map[string]string) error
	ListAgentRuns(ctx context.Context) ([]AgentRun, error)
	ListOpenPRs(ctx context.Context) ([]PR, error)
	ChecksFor(ctx context.Context, headSHA string) (Checks, error)
	// MergeStateFor reports whether a PR can land on its base.
	//
	// Separate from ListOpenPRs because the list endpoint does not carry
	// the field at all — GitHub computes it per PR, on demand, and only
	// the single-PR response has it. Called for the same narrow set as
	// ChecksFor, tickets sitting in Checks, so the snapshot stays a
	// bounded number of calls.
	MergeStateFor(ctx context.Context, number int) (MergeState, error)
	// FailedJobLogs returns the failing jobs for a head SHA with the tail
	// of each one's log. Separate from ChecksFor because every sweep calls
	// that one for every open PR, and logs are fetched for exactly one
	// ticket at the moment a rework run picks it up.
	FailedJobLogs(ctx context.Context, headSHA string) ([]JobLog, error)
	CreatePR(ctx context.Context, branch, title, body string, draft bool) (PR, error)
	MarkPRReady(ctx context.Context, number int) error
	// MergePR squash-merges and returns the merge commit SHA — the value
	// the post-deploy check compares against the platform (DESIGN §13).
	// Returns an error wrapping ErrNotMergeable when the branch conflicts
	// with its base, which is an ordinary outcome rather than a failure.
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
