// Package host defines the port to the code host (GitHub). The control
// plane reads runs and PRs through it to populate snapshots, and the agent
// harness writes PRs through it. The sim uses real temporary git
// repositories for repo state — git is local and free — so only the hosted
// surface (API calls) is faked.
package host

import (
	"context"
	"errors"
	"strings"
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
	// Outcome is how the run ended, for the runs whose conclusion this
	// project has measured. OutcomeUnknown for everything else,
	// including a run still live.
	Outcome RunOutcome
}

// PR is one open pull request.
type PR struct {
	Number  int
	Branch  string
	HeadSHA string
	Draft   bool
	URL     string
}

// BranchBelongsTo reports whether a branch names this ticket: the
// lowercased key appearing in it as a whole token, not as a substring.
//
// The token check is the whole point. `orc-1` is a prefix of `orc-19`
// and of `orc-181`, so a plain Contains would route ORC-19's PR to
// ORC-1 — and with three digits in play that is not a corner case, it
// is most of the board.
//
// One rule, and the callers that had been asking it separately now
// share it: the open-PR correlation in the snapshot build and the
// merged-PR lookup behind the hand-merge backfill. They are the same
// question about the same convention (DESIGN §5) and drift between
// them would route a ticket's PR one way and its merge commit another.
func BranchBelongsTo(branch, ticketKey string) bool {
	lb, lk := strings.ToLower(branch), strings.ToLower(ticketKey)
	if lk == "" {
		return false
	}
	for idx := 0; ; {
		j := strings.Index(lb[idx:], lk)
		if j < 0 {
			return false
		}
		start, end := idx+j, idx+j+len(lk)
		if (start == 0 || !isAlnum(lb[start-1])) && (end == len(lb) || !isAlnum(lb[end])) {
			return true
		}
		idx = end
	}
}

func isAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z'
}

// MergedPR is a PR that landed, and the commit it landed as.
type MergedPR struct {
	Number   int
	Branch   string
	MergeSHA string
	MergedAt time.Time
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

// RunOutcome is how an agent run ended, where the host can say.
//
// Measured against the live API on 2026-08-31, over Catapult's own
// design-agent runs: GitHub reports a cancelled run as `status:
// "completed"` with `conclusion: "cancelled"`, and a failed one the
// same way with `"failure"`. Before this existed the adapter read
// `status` alone, so all three of success, failure and cancellation
// reached the plane as the single fact `Live: false` — the run you had
// just stopped by hand was indistinguishable from one that finished
// cleanly.
//
// Deliberately not the whole of GitHub's vocabulary. It also documents
// `neutral`, `skipped`, `stale`, `timed_out`, `startup_failure` and
// `action_required`, none of which has been seen on this project's
// runs, and a fake or a mapping that enumerates what the real system
// may return is a claim about the real system. So the unmeasured ones
// map to OutcomeUnknown and are treated exactly as a run with no
// conclusion always was.
type RunOutcome string

const (
	// OutcomeUnknown: the host said nothing, or said something no one
	// here has measured. Never acted on.
	OutcomeUnknown   RunOutcome = ""
	OutcomeSucceeded RunOutcome = "succeeded"
	OutcomeFailed    RunOutcome = "failed"
	OutcomeCancelled RunOutcome = "cancelled"
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
	// RunID and RunAttempt identify the failing run itself, which the
	// URL cannot: a re-run keeps the same id and the same URL and only
	// increments the attempt. Both are needed — the id to ask for a
	// re-run, the attempt to tell one verdict from the next on the same
	// run. Zero when nothing failed.
	RunID      int64
	RunAttempt int
	// FailedJobs names the checks that are not green, so the failure
	// comment can say what broke. DESIGN §12 always described the comment
	// as "naming the failing jobs and linking the run"; for a long time it
	// only linked, and the link is the half an agent cannot follow.
	FailedJobs []string
}

// PreviewStatus is what the host knows about a branch's preview
// environment.
type PreviewStatus string

const (
	// PreviewNone: nothing has claimed to deploy this branch. A project
	// with no previews wired and a project whose preview has been torn
	// down are the same answer, and both mean "post nothing" (DESIGN §4).
	PreviewNone PreviewStatus = ""
	// PreviewPending: the platform has taken the branch and not finished.
	PreviewPending PreviewStatus = "pending"
	PreviewReady   PreviewStatus = "ready"
	PreviewFailed  PreviewStatus = "failed"
)

// Preview is a branch's preview environment as the code host reports it.
//
// Read from the host rather than from the deploy platform, because the
// platform is the one thing here that varies per project while the report
// does not: Render represents a PR preview as a GitHub deployment on the
// PR (its own changelog, 2024-09-09, replacing the PR comment it posted
// before), and so does anything else with a GitHub integration. It is
// also DESIGN §4's existing rule about the URL — reported by the
// publisher rather than derived from a branch name — holding for a
// publisher we no longer run ourselves.
type Preview struct {
	Status PreviewStatus
	// URL is where the preview is, set when Ready. It is the
	// environment_url the platform attached to its own deployment status;
	// nothing here builds it.
	URL string
	// Description is what the platform said, carried so a failure comment
	// can quote the platform rather than paraphrase it. Often empty.
	Description string
}

// JobLog is one failing CI job with the tail of its output. The dev
// agent's rework scope is the failure comment, and a comment that says
// "fix what the linked run reports" is only a scope to a reader who can
// open the run. The agent cannot: it holds no GitHub credential by
// design, and reading CI is not a reason to give it one. So the harness
// reads and the agent is handed the text — the same division as the
// non-asks document (DESIGN §4) and the ticket body itself.
// StatsRun is one Actions run as the stats collector records it
// (DESIGN §13).
//
// TicketKey and Kind are empty for every run that is not an agent run —
// CI on a pull request, a worker deploy, a preview build — and those are
// kept rather than dropped. They match no correlation convention and are
// plausibly most of the minutes spent, so measuring only the runs that
// name a ticket would measure a minority of the bill and call it the
// bill.
type StatsRun struct {
	ID         int64
	Repo       string
	Workflow   string
	Name       string
	Kind       string
	TicketKey  string
	StartedAt  time.Time
	Conclusion string
	Attempt    int
	// Complete distinguishes a run that has finished from one still in
	// flight. An unfinished run's minutes are not yet knowable, and
	// storing it with a zero would freeze that zero: the store is
	// insert-only, so the row a later pass would correct is a row it
	// cannot touch.
	Complete bool

	// DurationMS and BillableMS are left zero by the lister and filled
	// by the collector, which is the only thing that reads the run's
	// jobs. Split that way because the jobs call is per-run and the
	// expensive half of a backfill — the lister returns a hundred runs
	// per call and must not trigger a hundred more.
	DurationMS int64
	BillableMS int64
	JobCount   int
}

type JobLog struct {
	Name string
	URL  string
	// Log is a bounded tail, not the whole job. Whole logs are mostly
	// setup and are long enough to crowd out the argument they are
	// evidence for.
	Log string
	// Full is the whole log as fetched, for a caller that means to spill
	// it somewhere the agent can read. Never serialized: the claim file
	// is read by several things and a multi-megabyte log in it would be
	// carried by all of them to be used by none.
	//
	// It exists because the tail is not always the failure. Actions
	// appends post-job cleanup after the failing step, so the end of the
	// log is where the *runner* stopped rather than where the build
	// broke — measured on Catapult's ORC-224, whose last 150 lines are
	// checkout teardown, a Postgres service-container dump and orphan
	// cleanup, with no test output in them at all.
	Full string `json:"-"`
	// LogPath is where the caller wrote Full, as the caller named it, or
	// "". The prompt names it so the agent can read past the tail.
	LogPath string `json:",omitempty"`
	// Lines is how many lines Full had, so the prompt can say how much
	// the tail is not showing rather than leaving the agent to guess
	// whether the file is worth opening.
	Lines int `json:",omitempty"`
	// Errors are the failing-step markers found in Full, with where they
	// are. A path plus "go read it" is a worse prompt than a path plus a
	// line number: it makes finding the failure the agent's search
	// problem when the harness already holds the answer.
	Errors []LogMark `json:",omitempty"`
}

// LogMark is one notable line in a job log: where it is, which step it
// falls in, and what it says.
//
// Line is 1-based and counts lines of the log as written to disk, so it
// is the number `grep -n` and an editor both report for that file. The
// two agree by construction — the same string is scanned and spilled —
// and a test pins it, because a line number that is off by a header is
// worse than none.
type LogMark struct {
	Line int
	// Step is the enclosing `##[group]`'s title, which is the step name
	// as the workflow wrote it: "Run mix test", "Run mix credo --strict".
	Step string `json:",omitempty"`
	Text string
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
	// RerunRun asks for a completed run to run again. It is how a
	// verdict is refreshed when the thing that invalidated it was not a
	// commit — a mutex label the audit reads is the case that forced
	// this (DESIGN §12).
	//
	// Measured before being relied on, because the obvious worry is
	// real elsewhere: GitHub does not start a workflow run from an
	// *event* created with GITHUB_TOKEN. A re-run is an API instruction
	// rather than an event and is not covered by that rule — a re-run
	// issued with a workflow's own GITHUB_TOKEN and `actions: write`
	// took a run from attempt 1 to attempt 2, keeping `event:
	// pull_request`, with `github-actions[bot]` as the triggering
	// actor. The sweep already declares that permission.
	//
	// The new attempt is not visible immediately: `run_attempt` still
	// read 1 on a poll taken straight after the 201 and 2 about six
	// seconds later. A caller that reads once and concludes nothing
	// happened will be wrong.
	RerunRun(ctx context.Context, runID int64) error
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
	// MergedPRsFor returns the merged PRs belonging to a ticket, oldest
	// merge first, with their merge commit SHAs.
	//
	// Closed PRs, which is why ListOpenPRs cannot answer it: the case
	// this exists for is a PR the author merged by hand, and by the time
	// anything asks, it is closed and gone from that list.
	//
	// Correlated by the ticket key in the head branch, the same routing
	// DESIGN §5 already relies on — deriveBranch puts the lowercased key
	// at the front of every branch the pipeline names, so the key is the
	// join and nothing has to parse a commit subject to find it.
	MergedPRsFor(ctx context.Context, ticketKey string) ([]MergedPR, error)
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
	// PreviewFor reports the preview environment for a branch: where it
	// is, and whether it is there yet.
	//
	// By branch rather than by PR number because that is what separates a
	// preview from production. A ticket branch is never the default
	// branch, and production deploys land on the default branch, so
	// filtering on the ref cannot pick production up — which matters
	// because deleting or mis-reporting production would be the one
	// unrecoverable mistake here, the same reason §4's preview cleanup
	// names it explicitly rather than trusting a branch filter.
	//
	// Not on the deploy port: that port is one production platform per
	// project, and this is per branch and read from the host.
	PreviewFor(ctx context.Context, branch string) (Preview, error)
	// ReadFile returns a file's content on the default branch and
	// whether it exists at all. Missing is not an error: the boundary's
	// first pass on a milestone reads a note that is not there yet.
	ReadFile(ctx context.Context, path string) (string, bool, error)
	// PutFile commits one file to the default branch, creating it or
	// replacing what is there. The boundary's retro note uses it — the
	// boundary never opens a PR, being machinery rather than work.
	//
	// This used to be PutFileIfAbsent, refusing to touch an existing
	// note for re-run safety (DESIGN §10). That made the note whatever
	// the first pass over a milestone knew, permanently: a second pass
	// archived its tickets and their merge shas with nothing recording
	// them, which is the one failure the note exists to prevent.
	// Re-run safety now comes from merging by issue key instead, which
	// is idempotent for a resumed pass and additive for a new one.
	PutFile(ctx context.Context, path, content, message string) error
}
