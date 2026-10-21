package host

import "context"

// CheckRuns is the check-runs API, deliberately **not** part of Host.
//
// A fine-grained PAT cannot be granted the check-runs API at all — the
// endpoint appears nowhere in GitHub's fine-grained permissions
// reference, so no setting fixes it, and `runsForSHA` records what that
// cost: Catapult's ORC-7 died 35 seconds into a design claim on a 403
// reading another ticket's check runs, and sat in Designing until the
// stale-claim grace expired 23 minutes later. A workflow's own
// GITHUB_TOKEN *can* hold `checks: write`.
//
// So this is a separate interface rather than four more methods on Host,
// and that is the mechanical half of the rule. Host is what the plane
// holds; the sweep and every agent claim run under AGENT_GITHUB_TOKEN and
// build a full snapshot through it. If these methods were on Host they
// would be reachable from that path, and reachable means one refactor
// away from being called — a 403 that takes a claim down for a ticket the
// run has no interest in. Off Host, the type system says no, and the only
// caller is a command that runs inside a workflow job.
//
// Manual test verdicts are check runs (ops-free-pipeline.md §8.3 rule 7)
// because they are per-commit by construction, queryable as history, and
// natively red and green — which is what makes rule 8 decidable without a
// store of our own.
type CheckRuns interface {
	// CheckRunsFor returns this commit's check runs whose name starts
	// with prefix. Prefixed rather than exact because the caller wants
	// every manual-test verdict on a SHA at once, and one query beats one
	// per test.
	CheckRunsFor(ctx context.Context, sha, prefix string) ([]CheckRun, error)
	// CreateCheckRun records a completed verdict.
	CreateCheckRun(ctx context.Context, cr CheckRun) error
}

// CheckRunConclusion is the subset of GitHub's conclusions a verdict
// uses. Only these three are written here, and an unknown one read back
// is carried verbatim rather than mapped — the rule `agentRunsAt` learned
// about GitHub's unmeasured conclusions, which is that a value nobody has
// seen should behave as no value did.
type CheckRunConclusion string

const (
	CheckRunSuccess CheckRunConclusion = "success"
	CheckRunFailure CheckRunConclusion = "failure"
	// CheckRunNeutral is rule 8's verdict-about-the-judge: neither a pass
	// nor a failure of the thing under test, because what it reports is
	// that two runs disagreed about one commit. Failure would spend the
	// ticket's escalation budget on work that was never broken (§12's
	// two), and success would bury the disagreement.
	CheckRunNeutral CheckRunConclusion = "neutral"
)

// CheckRun is one verdict on one commit.
type CheckRun struct {
	// SHA is the commit judged.
	SHA string
	// Name is what the verdict is about, and it has to be stable across
	// commits for rule 8 to compare two runs' answers about one test.
	Name       string
	Conclusion CheckRunConclusion
	// Title and Summary are what a reader sees in the Checks tab.
	Title   string
	Summary string
	// DetailsURL points at the evidence — the run whose artifacts hold
	// the screenshots, the trace, the transcript. **A verdict with no
	// evidence is a failure** (§8.3 rule 3), and this field is where that
	// rule becomes checkable rather than aspirational.
	DetailsURL string
}
