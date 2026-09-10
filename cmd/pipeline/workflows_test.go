package main

import (
	"github.com/SwaggerAllen/orchestration/internal/host/github"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Declaring a permissions block drops every permission it does not
// name, so a workflow that checks code out and declares one must name
// contents. Getting this wrong has now cost two debugging rounds, and
// neither error said "permission": the sweep reported HTTP 403 from
// the GitHub API, and checkout of a private repo reported "Repository
// not found", which reads as a bad URL.
//
// A workflow with no permissions block at all is not flagged — that is
// the deliberate choice to inherit the repository default.
func TestCheckoutWorkflowsGrantContents(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	for _, dir := range []string{
		filepath.Join(root, ".github", "workflows"),
		filepath.Join(root, "examples", "stubs"),
	} {
		found, err := filepath.Glob(filepath.Join(dir, "*.yml"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, found...)
	}
	if len(files) == 0 {
		t.Fatal("found no workflows to check — the paths must have moved")
	}

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// Comments are stripped first: the comment explaining why a
		// workflow needs contents: mentions contents:, which is enough
		// to make a naive text search pass on a file that grants none.
		body := stripComments(string(raw))
		if !strings.Contains(body, "actions/checkout") {
			continue
		}
		if !strings.Contains(body, "\npermissions:") {
			continue // inherits the repository default, deliberately
		}
		if !strings.Contains(body, "contents:") {
			t.Errorf("%s checks out code and declares permissions without contents — checkout will fail, and on a private repo it will say the repository does not exist", filepath.Base(f))
		}
	}
}

// stripComments drops YAML comments so a sentence about a permission
// cannot stand in for the permission. Crude — it does not know about
// '#' inside a quoted string — but the workflows here have none, and a
// false positive is a test that fails loudly rather than one that
// passes quietly.
func stripComments(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if h := strings.IndexByte(l, '#'); h >= 0 {
			lines[i] = l[:h]
		}
	}
	return strings.Join(lines, "\n")
}

// Where the retro notes are cleared is the whole safety argument, so it
// is asserted rather than left to whoever next edits the step.
//
// The notes are the reset's only record of what a milestone boundary
// archived, and they have to outlive a reset that could not finish. A
// conflicting revert exits the step, so clearing them after the revert
// loop means a re-run still has them; clearing them before it would
// destroy the answer on exactly the run that needed it twice. They also
// have to be cleared before the push, or the commit never leaves the
// runner and the next boundary skips its note again.
// The worker suite gates the merge, not only the deploy.
//
// `worker-deploy.yml` runs it and always has, under a comment saying the
// signature check "is not deployed untested" — but that workflow fires
// on `push: branches: [main]` and never on a pull request. So a PR
// breaking a worker test used to pass CI, merge, and fail the deploy.
//
// That is the worst place for it to land. A red deploy leaves the
// metronome serving the previous version: no project visibly loses its
// beat and nothing reports it, which is the failure worker-deploy.yml's
// own header says the automatic deploy exists to prevent. The test
// caught the bug in the one place where failing looks identical to the
// thing it was protecting against.
//
// Asserted here because a step nobody checks is a step a later cleanup
// deletes as duplication — it does duplicate worker-deploy.yml, and the
// duplication is the point.
func TestCIRunsTheWorkerSuiteOnPullRequests(t *testing.T) {
	ci := repoFile(t, filepath.Join(".github", "workflows", "ci.yml"))

	if !strings.Contains(ci, "pull_request") {
		t.Fatal("ci.yml no longer runs on pull requests; nothing below gates a merge")
	}
	deploy := repoFile(t, filepath.Join(".github", "workflows", "worker-deploy.yml"))

	// The glob, not a filename. This asserted `index.test.ts` until a
	// second suite was added, and a named file is a gate that silently
	// stops covering whatever arrives next to it: the new suite runs
	// locally, passes, and is executed by neither the merge gate nor the
	// deploy gate, so a green pipeline says nothing at all about it.
	//
	// A directory argument is the wrong fix and was the reason the
	// filename was there — Node tries to load `worker` as a module and
	// fails.
	const workerSuite = "node --test --experimental-strip-types *.test.ts"
	for _, w := range []struct{ name, body string }{{"ci.yml", ci}, {"worker-deploy.yml", deploy}} {
		if !strings.Contains(w.body, workerSuite) {
			t.Errorf("%s does not run the worker suite as %q. Named alone, ci.yml would gate "+
				"the deploy and not the merge — a PR breaking it merges clean and fails the "+
				"deploy, where a failure is indistinguishable from a metronome nobody "+
				"redeployed. Named as one file, a suite added beside it is never run at all.",
				w.name, workerSuite)
		}
		// Same runtime in both, or they can disagree: the merge gate
		// passes and the deploy gate still fails after it.
		if !strings.Contains(w.body, `node-version: "22"`) {
			t.Errorf("%s does not pin node 22; the merge gate and the deploy gate would "+
				"test different runtimes", w.name)
		}
	}
}

// The gate is pinned in prose as well as in the two workflows, and a
// change to one that misses the others leaves a contributor running
// something CI does not. Six consecutive design-review rounds in the
// project this pipeline drives each corrected one statement of a rule
// and left its siblings; this is the same shape, in this repo.
func TestTheWorkerSuiteCommandIsPinnedEverywhereItIsStated(t *testing.T) {
	const workerSuite = "node --test --experimental-strip-types *.test.ts"
	for _, path := range []string{
		filepath.Join(".github", "workflows", "ci.yml"),
		filepath.Join(".github", "workflows", "worker-deploy.yml"),
		"CLAUDE.md",
		filepath.Join("worker", "README.md"),
	} {
		body := repoFile(t, path)
		if !strings.Contains(body, workerSuite) {
			t.Errorf("%s does not state the worker suite as %q — the four statements of this "+
				"command have drifted, so what a contributor runs is not what CI runs", path, workerSuite)
		}
	}
}

func TestRehearsalResetClearsRetroNotesBetweenTheRevertsAndThePush(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "rehearse.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(raw))

	revert := strings.Index(body, "git revert --no-edit")
	clear := strings.Index(body, "git rm -r -q docs/retros")
	push := strings.Index(body, "git push origin main")
	if revert < 0 || clear < 0 || push < 0 {
		t.Fatalf("rehearse.yml no longer reverts (%d), clears the notes (%d) or pushes (%d) — one of them was renamed or dropped", revert, clear, push)
	}
	if clear < revert {
		t.Error("the retro notes are cleared before the reverts run: a conflicting revert exits the step, and the re-run would come back to notes that are already gone")
	}
	if push < clear {
		t.Error("the retro notes are cleared after the push: the deletion never leaves the runner, and the next boundary skips writing its note")
	}
}

// The reset's baseline is the newest GitHub release, read from the API,
// never a tag in the checkout. A `seed` tag has to be pushed from a
// clone, so it went stale the moment the scaffold moved without one —
// it was last moved before the port that changed every doc — and a
// release is cut from the repository's own page. The step must also be
// able to read the API: the same token the checkout used.
func TestTheRehearsalBaselineIsTheLatestRelease(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "rehearse.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(raw))
	step := strings.Index(body, "name: revert what the last rehearsal merged")
	if step < 0 {
		t.Fatal("the revert step was renamed")
	}
	rest := body[step:]
	for _, want := range []string{
		"/releases/latest",
		"jq -r .tag_name",
		`GH_TOKEN: ${{ secrets.REHEARSAL_REPO_TOKEN }}`,
		`git rev-list --max-parents=0 HEAD`,
	} {
		if !strings.Contains(rest, want) {
			t.Errorf("the revert step does not carry %s — the baseline summary would measure from the wrong place, or fail to read the API", want)
		}
	}
	if strings.Contains(body, "refs/tags/seed") {
		t.Error("rehearse.yml still reads a seed tag — that tag has to be pushed from a clone, and it was stale the last time anyone read it")
	}
}

// A stub that runs `pipeline setup` must grant deployments: read.
//
// Setup ends by probing the configured deploy endpoint, which for
// provider "github" is GET /repos/{owner}/{repo}/deployments. That probe
// is the point of doing it at hookup — nothing else exercises deploy
// detection until a ticket reaches Merged, hours later, where the
// symptom is "stuck in Merged" and names neither the endpoint nor the
// token.
//
// The admin stub shipped without the scope and a *dry run* failed on it,
// which is the worst version: the run wrote nothing, printed a correct
// three-action plan, and then 403'd on a read. The env block already
// passed DIGITALOCEAN_TOKEN and GITHUB_TOKEN for that same probe, so
// both providers were in mind — the DigitalOcean path needs only a
// secret, the GitHub path needs a secret and a scope, and one half was
// wired.
func TestSetupStubsGrantDeploymentsRead(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "examples", "stubs", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("found no stubs to check — the path must have moved")
	}
	checked := 0
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		body := stripComments(string(raw))
		if !strings.Contains(body, "pipeline setup") {
			continue
		}
		checked++
		if !strings.Contains(body, "\npermissions:") {
			continue // inherits the repository default, deliberately
		}
		if !strings.Contains(body, "deployments:") {
			t.Errorf("%s runs pipeline setup and declares permissions without deployments — setup's deploy probe will 403, on a dry run as readily as on an apply", filepath.Base(p))
		}
	}
	if checked == 0 {
		t.Error("no stub runs pipeline setup — the command moved and this test now checks nothing")
	}
}

// Every agent action's abort must hand the model run's captured output
// to the ticket.
//
// The comment used to be the run URL and nothing else, so a run that
// exited 249 reached the author as a bare number: the code is the
// CLI's, not this harness's, and the output that could have explained
// it was being discarded. A fifth agent action added without this line
// would quietly reintroduce that, since the abort still works — it just
// says nothing.
// The abort carries the model's own argument, and points at the file
// that agent's model actually writes.
//
// Both halves are load-bearing and the second is the one that bites. The
// outcome file is not uniform: design and dev write outcome.json,
// reconcile writes verdict.json, and boundary writes proposals.json,
// which carries no single prepared argument and so is deliberately not
// wired. A first pass at this pointed all four at outcome.json — which
// fails silently, because an absent file is the ordinary case and
// appends nothing. It would have looked wired and rescued nothing on
// two of the four.
func TestAgentActionsHandTheModelsPreparedArgumentToTheAbort(t *testing.T) {
	// The file each kind's model run is told to write, and therefore the
	// only file its abort may name.
	for _, c := range []struct{ kind, writer, outcome string }{
		{"design", "outcome-path", "outcome.json"},
		{"dev", "outcome-path", "outcome.json"},
		// reconcile names its own: --verdict-path to write, --verdict to
		// read back. Spelled out rather than assumed — assuming
		// outcome-path here is what this test caught on its first run.
		{"reconcile", "verdict-path", "verdict.json"},
	} {
		body := repoFile(t, filepath.Join(".github", "actions", "agent-"+c.kind, "action.yml"))
		abort := body[strings.Index(body, "agent abort"):]
		want := `--outcome "$RUNNER_TEMP/pipeline/` + c.outcome + `"`
		if !strings.Contains(abort, want) {
			t.Errorf("agent-%s's abort does not carry %s — a run that failed validation loses the "+
				"reasoning it had already written, and the ticket goes back with a stack trace and "+
				"no argument", c.kind, want)
		}
		if !strings.Contains(body, "--"+c.writer+` "$RUNNER_TEMP/pipeline/`+c.outcome+`"`) {
			t.Errorf("agent-%s's abort names %s but its model run is not told to write it; the abort "+
				"would read a file nobody produces and append nothing, silently", c.kind, c.outcome)
		}
	}
	// Boundary is the deliberate omission. Asserted so it stays a
	// decision rather than becoming an oversight somebody "fixes" by
	// pointing it at a file it never writes.
	boundary := repoFile(t, filepath.Join(".github", "actions", "agent-boundary", "action.yml"))
	if strings.Contains(boundary[strings.Index(boundary, "agent abort"):], "--outcome ") {
		t.Error("agent-boundary's abort names an outcome file; its model writes proposals.json, " +
			"which carries no single prepared argument (see WithPreparedSummary)")
	}
}

func TestAgentActionsHandTheModelRunsOutputToTheAbort(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", ".github", "actions", "agent-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("found no agent actions to check — the paths must have moved")
	}
	for _, d := range dirs {
		raw, err := os.ReadFile(filepath.Join(d, "action.yml"))
		if err != nil {
			t.Fatal(err)
		}
		body := stripComments(string(raw))
		i := strings.Index(body, "--reason failed")
		if i < 0 {
			t.Errorf("%s: no abort invocation", d)
			continue
		}
		if !strings.Contains(body[i:], "--error-file \"$RUNNER_TEMP/pipeline/run-error.txt\"") {
			t.Errorf("%s: the abort reports a failure without the output that explains it", d)
		}
	}
}

// The other half: the step that lands the outcome has to capture its
// own stderr into the same file. A finish that dies — an outcome that
// will not validate, a tracker that will not answer — reached the
// author as "run failed: <url>" with the Go error that said exactly
// what went wrong left in a collapsed step.
func TestAgentActionsCaptureTheFinishStepsStderr(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", ".github", "actions", "agent-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("found no agent actions to check — the paths must have moved")
	}
	for _, d := range dirs {
		raw, err := os.ReadFile(filepath.Join(d, "action.yml"))
		if err != nil {
			t.Fatal(err)
		}
		body := stripComments(string(raw))
		// Redirected AND replayed: capturing without the cat back would
		// take the failure out of the workflow log to put it on the
		// ticket, which trades one blind spot for another.
		if !strings.Contains(body, `2> "$RUNNER_TEMP/pipeline/run-error.txt"`) {
			t.Errorf("%s: the finish step's stderr is not captured", d)
		}
		if !strings.Contains(body, `cat "$RUNNER_TEMP/pipeline/run-error.txt" >&2`) {
			t.Errorf("%s: captured stderr is never replayed to the log", d)
		}
	}
}

// The capture is only useful if something writes the file the abort
// reads, and only honest if a run that recovered on the failover does
// not leave the first attempt's death behind to be read as its cause.
func TestModelRunCapturesItsOutputAndClearsItOnSuccess(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "actions", "run-agent-model", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(raw))
	for _, want := range []string{
		`ERRFILE="$LOGDIR/run-error.txt"`,
		`rm -f "$ERRFILE" "$CODEFILE"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("run-agent-model does not %q", want)
		}
	}
	// Two clears: once before the first attempt, once after a failover
	// that succeeded. One of them alone leaves a stale cause on a ticket.
	if n := strings.Count(body, `rm -f "$ERRFILE" "$CODEFILE"`); n < 2 {
		t.Errorf("the evidence is cleared %d time(s); a recovered failover leaves the first attempt behind", n)
	}
}

// The prompt must reach the model on stdin, never as an argument.
//
// Linux caps a single argv element at MAX_ARG_STRLEN (32 pages, 131072
// bytes), independently of the much larger ARG_MAX for the whole
// vector. The prompt inlines the ticket, its comments, the repo context
// and the confirmed non-asks, so it crosses that on a real project:
// Catapult's ORC-84 died 35ms into three consecutive design runs with
// "/usr/bin/env: Argument list too long", the credential failover
// dutifully retrying into the identical wall, having never invoked the
// model at all.
//
// Guarded by a test because `-p "$PROMPT"` is the obvious way to write
// this and reads as correct until the prompt gets big.
func TestModelRunPassesThePromptOnStdin(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "actions", "run-agent-model", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(raw))
	if strings.Contains(body, `"$PROMPT"`) {
		t.Error("the prompt is interpolated into the command line; over 128KiB that is E2BIG before the model is reached")
	}
	// Two attempts, subscription and API key, and the failover has to
	// re-read the file rather than inherit a drained stream.
	//
	// The redirect reads a parameter rather than $PROMPT_PATH directly,
	// since the resume feeds a different file through the same two arms
	// — so the invariant is asserted where it now lives: both arms
	// redirect, and the ordinary pass hands them the prompt file.
	if n := strings.Count(body, `< "$stdin"`); n != 2 {
		t.Errorf("the prompt is redirected into %d attempt(s), want both", n)
	}
	if !strings.Contains(body, `attempt "$mode" "$PROMPT_PATH"`) {
		t.Error("the ordinary attempt does not read the prompt file")
	}
}

// An exit 0 from the CLI means the turn ended, which is not the same
// event as the pass being finished: `-p` is non-interactive, nothing
// waits on a command the model put in the background, and a pass that
// stops mid-work exits 0 and reads as a success. Catapult's ORC-230 went
// to Blocked twice inside twenty-five minutes that way, on consecutive
// dev reworks that each ended on a sentence about waiting.
//
// The role's required artifact is the signal, and the resume is bounded
// at one. Guarded because the shape is easy to "simplify" into either of
// its two broken neighbours: a loop, or a failure — and failing is the
// worse of them, because the steps after this one are what commit and
// push the tree.
func TestModelRunResumesATurnThatEndedEarly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "actions", "run-agent-model", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(raw))
	for _, want := range []string{
		"EXPECT_FILE: ${{ inputs.expect_file }}",
		`resume_if_unfinished "$mode"`,
		`attempt "$mode" "$RESUME_PATH" --continue`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("run-agent-model does not %q", want)
		}
	}
	// One resume, not a loop: a pass that will not finish must not be
	// paid for over and over.
	if n := strings.Count(body, "--continue"); n != 1 {
		t.Errorf("--continue appears %d time(s); the resume is bounded at one", n)
	}
	// And the resume must not decide the step's fate. Skipping the push
	// loses the run's work outright, which is the older failure its
	// safety net exists for, so a resume that comes back empty exits 0
	// and leaves the report to finish.
	fn := body[strings.Index(body, "resume_if_unfinished() {"):]
	fn = fn[:strings.Index(fn, "\n        }\n")]
	if strings.Contains(fn, "exit ") {
		t.Error("the resume exits the step; that skips the push and discards the run's work")
	}
	if !strings.Contains(fn, "|| true") {
		t.Error("a resume that errors is not tolerated; an unverified flag must not be able to fail the step")
	}
}

// The resume is only armed for the roles whose output IS a file. Dev's
// hand-back and reconcile's verdict are each required by finish, so
// their absence is a stopped pass; design's product is the tree it
// wrote and boundary's proposals file is optional by construction, so
// neither has a signal to read and neither claims one.
func TestOnlyTheRolesWithARequiredArtifactDeclareIt(t *testing.T) {
	for _, tc := range []struct {
		action string
		expect string
	}{
		{"agent-dev", "${{ runner.temp }}/pipeline/handback.md"},
		{"agent-reconcile", "${{ runner.temp }}/pipeline/verdict.json"},
		{"agent-design", ""},
		{"agent-boundary", ""},
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "actions", tc.action, "action.yml"))
		if err != nil {
			t.Fatal(err)
		}
		body := stripComments(string(raw))
		got := strings.Contains(body, "expect_file:")
		if want := tc.expect != ""; got != want {
			t.Errorf("%s declares expect_file = %v, want %v", tc.action, got, want)
		}
		if tc.expect != "" && !strings.Contains(body, "expect_file: "+tc.expect) {
			t.Errorf("%s does not point expect_file at %s — the file finish reads back", tc.action, tc.expect)
		}
	}
}

// The CLI must come from the stable dist-tag, resolved and printed.
//
// `@latest` made every agent run in every project depend on a
// third-party publish that can land at any moment with no commit
// anywhere in this system, and on 2026-08-19 one did: 2.1.237 went out
// at 23:57Z and reports "claude native binary not installed" when
// installed this way. Catapult's ORC-6 hit it an hour later and every
// agent run in the project failed identically.
//
// Guarded because `@latest` is the obvious thing to write and reads as
// helpful right up until a publish nobody here made takes the pipeline
// down.
func TestModelRunFollowsTheStableChannel(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "actions", "run-agent-model", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(raw))
	if strings.Contains(body, "claude-code@latest") {
		t.Error("the CLI is installed from @latest; a publish nobody here made can take every project down")
	}
	if !strings.Contains(body, `CLI_TAG="stable"`) {
		t.Error("the CLI does not follow the stable dist-tag")
	}
	// An unresolvable tag must not become `claude-code@`, which installs
	// nothing and fails as if the model had.
	if !strings.Contains(body, `CLI_FALLBACK="`) {
		t.Error("no fallback for a registry that cannot be asked")
	}
	// The tag is what this follows; the number is what a failed run has
	// to be able to name. ORC-6 was diagnosed by bracketing the break
	// against a version nobody had recorded.
	if !strings.Contains(body, `echo "claude-code $CLI_VERSION`) {
		t.Error("the resolved version is never printed, so a failed run cannot say which CLI it ran")
	}
	// Both attempts, subscription and API key, or the failover silently
	// runs a different version from the one that was tested.
	if n := strings.Count(body, `"@anthropic-ai/claude-code@$CLI_VERSION"`); n != 2 {
		t.Errorf("the pin reaches %d attempt(s), want both", n)
	}
}

// The stub and the correlation regexp live in different files, and only
// one of them can fail a test. This reads the stub's own run-name and
// checks the name it gives a detached run against the real regexp, so
// renaming it to something that correlates is caught here rather than by
// a detached run quietly attaching itself to the protocol.
func TestTheStubsDetachedRunNameCannotCorrelate(t *testing.T) {
	stub := repoFile(t, filepath.Join("examples", "stubs", "pipeline-live-suite.yml"))

	// The literal the stub falls back to when no ticket is given. Pulled
	// out of the file rather than restated, which is the whole point.
	i := strings.Index(stub, "|| '")
	if i < 0 {
		t.Fatal("the stub's run-name has no `|| '<literal>'` no-ticket fallback. Either it lost one — a detached run would then be named \"pipeline: live-suite \" and half-match the convention — or the fallback is now an expression this test cannot read, in which case it also cannot check that it fails to correlate")
	}
	rest := stub[i+len("|| '"):]
	j := strings.Index(rest, "'")
	if j < 0 {
		t.Fatal("could not read the fallback run name out of the stub")
	}
	detached := rest[:j]

	if m := github.RunNameFields(detached); m != nil {
		t.Errorf("the stub names a detached run %q, which correlates as kind %q ticket %q — it would reach the plane as an agent run",
			detached, m[0], m[1])
	}
	// And the ticketed branch is still the exact convention.
	if !strings.Contains(stub, "format('pipeline: live-suite {0}', inputs.ticket)") {
		t.Error("the stub no longer builds the ticketed run name as \"pipeline: live-suite <ticket>\", which is the correlation")
	}
}

// The record review runs as a second model pass inside the design job
// (DESIGN §4), and the shape of that step is what keeps it a proposal
// rather than a gate: through the shared runner, continue-on-error, and
// its own log directory so its death is not the design run's.
func TestTheRecordReviewRunsThroughTheSharedRunnerAndCannotFailThePass(t *testing.T) {
	body := stripComments(repoFile(t, filepath.Join(".github", "actions", "agent-design", "action.yml")))
	i := strings.Index(body, "name: run the record review")
	if i < 0 {
		t.Fatal("the design action has no record review step")
	}
	// The step's own block: up to the next step.
	j := strings.Index(body[i+1:], "\n    - name:")
	step := body[i:]
	if j >= 0 {
		step = body[i : i+1+j]
	}
	for _, want := range []struct{ text, why string }{
		{"actions/run-agent-model@", "its credential policy is the shared runner's, not its own"},
		{"continue-on-error: true", "a reviewer that dies must not fail a good design pass (DESIGN §4)"},
		{"log_dir: ${{ runner.temp }}/pipeline/review", "its death rattle must not land where the design abort reads this run's cause"},
		{"steps.record.outputs.reviewable == 'true'", "no model run when the pass wrote nothing under designOwnedPaths"},
	} {
		if !strings.Contains(step, want.text) {
			t.Errorf("the record review step does not carry %q — %s", want.text, want.why)
		}
	}
	// And the finish step reads both files the review can leave behind.
	finish := body[strings.Index(body, "pipeline agent finish"):]
	for _, want := range []string{`--review "$RUNNER_TEMP/pipeline/review/review.json"`, `--review-error "$RUNNER_TEMP/pipeline/review/run-error.txt"`} {
		if !strings.Contains(finish, want) {
			t.Errorf("the finish step does not pass %s; the review would run and never be read", want)
		}
	}
}

// A declined pass has nothing to preview, and a preview build installs
// a toolchain and spends a metered deployment. All three preview steps
// are gated, not one: a build with no publish wastes the toolchain, a
// publish with no build fails.
func TestThePreviewIsSkippedOnARecordReviewDecline(t *testing.T) {
	body := stripComments(repoFile(t, filepath.Join(".github", "actions", "agent-design", "action.yml")))
	for _, step := range []string{"build the storybook export", "read the preview target", "publish the preview"} {
		i := strings.Index(body, "name: "+step)
		if i < 0 {
			t.Fatalf("no step %q", step)
		}
		block := body[i:]
		if j := strings.Index(block[1:], "\n    - name:"); j >= 0 {
			block = block[:j+1]
		}
		if !strings.Contains(block, "steps.verdict.outputs.verdict != 'decline'") {
			t.Errorf("step %q runs on a declined pass", step)
		}
	}
}

// run-agent-model's log directory is an input with the old path as its
// default, so the four agent actions that pass none keep writing where
// their abort steps read.
func TestTheModelRunnerTakesALogDirAndDefaultsToTheAbortsPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "actions", "run-agent-model", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(raw))
	for _, want := range []string{
		"  log_dir:",
		"LOG_DIR: ${{ inputs.log_dir }}",
		`LOGDIR="${LOG_DIR:-$RUNNER_TEMP/pipeline}"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("run-agent-model does not carry %q", want)
		}
	}
}

// The record review is handed the tree from the pass's start commit,
// not the merge-base, and the export precedes the assembly that reads
// it. `steps.base.outputs.sha` is the wrong sha for it: on a second pass
// after a decline the first pass may already have amended an entry.
func TestTheRecordReviewIsHandedTheTreeFromThePassStart(t *testing.T) {
	body := stripComments(repoFile(t, filepath.Join(".github", "actions", "agent-design", "action.yml")))
	export := strings.Index(body, "name: export the record as it stood at the pass's start")
	assemble := strings.Index(body, "name: assemble the record review")
	if export < 0 || assemble < 0 || export > assemble {
		t.Fatalf("export at %d, assemble at %d — the export must precede the step that reads it", export, assemble)
	}
	step := body[export:assemble]
	for _, want := range []struct{ text, why string }{
		{`BEFORE="${{ steps.branch.outputs.before }}"`, "the pass's start commit, not the merge-base"},
		{`git cat-file -e "$BEFORE:$d"`, "git archive fails on a pathspec matching nothing"},
		{`git archive "$BEFORE" -- "$d" | tar -x -C "$DEST"`, "the export itself"},
	} {
		if !strings.Contains(step, want.text) {
			t.Errorf("the export step does not carry %s — %s", want.text, want.why)
		}
	}
	if strings.Contains(step, "steps.base.outputs.sha") {
		t.Error("the export reads the merge-base; a second pass after a decline would be shown the first pass's amendment as its own")
	}
	if !strings.Contains(body[assemble:], `--base-tree "$RUNNER_TEMP/pipeline/record-base"`) {
		t.Error("the record review is not handed the exported tree; the section would say no base tree was handed in")
	}
}

// Reconcile's reprompt runs after its checkout and before the model,
// with the merge-base tree and the branch's changed files.
func TestReconcileRepromptsAfterItsCheckoutWithTheTouchedReasons(t *testing.T) {
	body := stripComments(repoFile(t, filepath.Join(".github", "actions", "agent-reconcile", "action.yml")))
	checkout := strings.Index(body, `git checkout "$BRANCH"`)
	reprompt := strings.Index(body, "name: export the record at the merge-base")
	model := strings.Index(body, "name: run the reconcile agent")
	if checkout < 0 || reprompt < 0 || model < 0 || !(checkout < reprompt && reprompt < model) {
		t.Fatalf("checkout at %d, reprompt at %d, model at %d — the reprompt needs the branch and the model needs the reprompt", checkout, reprompt, model)
	}
	step := body[reprompt:model]
	for _, want := range []string{
		`BASE="$(git merge-base origin/main HEAD)"`,
		`git cat-file -e "$BASE:$d"`,
		`git diff --name-only origin/main...HEAD > "$RUNNER_TEMP/pipeline/changed.txt"`,
		"pipeline agent reprompt",
		"--kind reconcile",
		`--verdict-path "$RUNNER_TEMP/pipeline/verdict.json"`,
		`--base-tree "$DEST"`,
		`--changed-files "$RUNNER_TEMP/pipeline/changed.txt"`,
	} {
		if !strings.Contains(step, want) {
			t.Errorf("the reconcile reprompt step does not carry %s", want)
		}
	}
}

// The replay reads a project and posts nothing: no tracker credential,
// no finish, no comment. It is a measurement of the reviewer, and a
// measurement that wrote to the thing it measured would not be one.
//
// Two files, because the stub is what a project copies and the action
// is what a fix reaches: the stub wires three jobs, the action does
// each one's work. Both are held to the absence list.
func TestTheReviewReplayReadsAProjectAndPostsNothing(t *testing.T) {
	action := stripComments(repoFile(t, filepath.Join(".github", "actions", "review-replay", "action.yml")))
	stub := stripComments(repoFile(t, filepath.Join("examples", "stubs", "pipeline-review-replay.yml")))
	for _, want := range []struct{ text, why string }{
		{"pipeline agent record-review", "the same assembly the design action runs"},
		{`--base-tree "$RUNNER_TEMP/pipeline/record-base"`, "the reviewer is handed the record as it stood before the pass"},
		{"actions/run-agent-model", "the same runner, so the verdict is the one a real pass would get"},
		{`git cat-file -e "$BEFORE:$d"`, "the export guard, for a pass whose start had no screens/"},
		{"xargs -r -I{} git show --format= --cc {} -- '*.md'", "the first-parent, markdown-only extraction the action uses"},
		{"actions/upload-artifact", "the verdicts are the output"},
		{`--prompt-template "$GITHUB_WORKSPACE/.pipeline/prompts/record-review.md"`, "the prompt under test is the one pipeline_ref names"},
		{"name: replay-config", "today's config rides an artifact from the list phase"},
		{`git log --first-parent --format=%H -n "$MERGES" HEAD`, "the count is git's own -n; `| head` under pipefail kills git with SIGPIPE on a repo with more merges than the cap (catapult's first replay exited 141 with nothing listed)"},
		{"shopt -s nullglob", "a run with no verdicts renders a table that says so rather than failing on the literal glob"},
		{`cp "$RUNNER_TEMP/pipeline/config/pipeline.config.json" "$GITHUB_WORKSPACE/pipeline.config.json"`, "the merge's own config is overlaid before the assemble — 8 of the dummy's first 10 replays failed validation on a config that predated ready_for_design"},
	} {
		if !strings.Contains(action, want.text) {
			t.Errorf("review-replay/action.yml does not carry %s — %s", want.text, want.why)
		}
	}
	for _, want := range []struct{ text, why string }{
		{"max-parallel: 1", "one model run at a time"},
		{"fetch-depth: 0", "the reconstruction walks back from the merge"},
		{"pipeline_ref: ${{ inputs.pipeline_ref }}", "the reviewer under test is chosen per run, not per edit"},
		{"actions/review-replay@main", "the reconstruction is the action's, so a fix to it reaches every project"},
	} {
		if !strings.Contains(stub, want.text) {
			t.Errorf("pipeline-review-replay.yml does not carry %s — %s", want.text, want.why)
		}
	}
	if strings.Contains(action, "| head") {
		t.Error("review-replay/action.yml pipes git into head — under pipefail that is SIGPIPE and exit 141 the moment the repo has more merges than the count")
	}
	for name, body := range map[string]string{"review-replay/action.yml": action, "pipeline-review-replay.yml": stub} {
		for _, absent := range []string{"pipeline agent finish", "LINEAR_API_KEY", "PIPELINE_STATE_TOKEN", "CommentTicket", "contents: write", "GITHUB_TOKEN:"} {
			if strings.Contains(body, absent) {
				t.Errorf("%s carries %q — a replay that writes to what it measures is not a measurement", name, absent)
			}
		}
	}
}

// A pass's findings are callouts (prompts/record-review.md): posted,
// acted on by nobody. The first catapult replay counted one in the
// narration column, so a pass read as a pass with a finding. The row
// counts findings the plane acts on and callouts apart.
func TestTheReplayTableCountsAPassesFindingsAsCallouts(t *testing.T) {
	action := stripComments(repoFile(t, filepath.Join(".github", "actions", "review-replay", "action.yml")))
	for _, want := range []struct{ text, why string }{
		{`if [ "$verdict" = decline ]; then`, "the narration and contradiction counts are taken on a decline only"},
		{`echo "callouts=$(jq '.findings | length'`, "a pass's findings are counted as callouts"},
		{"| narration | contradiction | callouts |", "the table shows them apart"},
	} {
		if !strings.Contains(action, want.text) {
			t.Errorf("review-replay/action.yml does not carry %s — %s", want.text, want.why)
		}
	}
}
