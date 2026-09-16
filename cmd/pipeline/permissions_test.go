package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A called workflow cannot be granted more than the calling job holds.
// A stub with no permissions block inherits the repository default,
// which is restrictive on a new repo, so a callee asking for write dies
// before a runner exists — "startup_failure", no job, no logs, nothing
// in the UI to read. It cost a live debugging round to find, and the
// only visible symptom was an agent that never ran.
func TestStubsGrantWhatTheirReusableWorkflowsAskFor(t *testing.T) {
	root := filepath.Join("..", "..")
	stubs, err := filepath.Glob(filepath.Join(root, "examples", "stubs", "*.yml"))
	if err != nil || len(stubs) == 0 {
		t.Fatalf("found no stubs to check: %v", err)
	}
	uses := regexp.MustCompile(`uses:\s*SwaggerAllen/orchestration/\.github/workflows/([\w-]+\.yml)@`)

	checked := 0
	for _, stub := range stubs {
		body, err := os.ReadFile(stub)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range uses.FindAllStringSubmatch(string(body), -1) {
			callee := m[1]
			calleeBody, err := os.ReadFile(filepath.Join(root, ".github", "workflows", callee))
			if err != nil {
				t.Errorf("%s calls %s, which is not in this repo: %v", filepath.Base(stub), callee, err)
				continue
			}
			checked++
			caller := permissionsOf(string(body))
			for scope, want := range permissionsOf(string(calleeBody)) {
				if rank(caller[scope]) < rank(want) {
					got := caller[scope]
					if got == "" {
						got = "nothing (no permissions block, or the scope is unnamed)"
					}
					t.Errorf("%s grants %s %s but %s asks for %s — the run will fail at startup with no job to inspect",
						filepath.Base(stub), scope, got, callee, want)
				}
			}
		}
	}
	if checked == 0 {
		t.Error("matched no reusable-workflow calls — the regex has drifted from the stubs")
	}
}

// permissionsOf reads a workflow's top-level permissions block. Written
// by hand because this repo carries no dependencies; the workflows are
// uniformly formatted, and a shape it cannot read returns nothing, which
// fails the comparison loudly rather than passing it quietly.
func permissionsOf(body string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "permissions:") {
			continue
		}
		if strings.Contains(line, "{}") {
			return out // explicitly nothing
		}
		for _, l := range lines[i+1:] {
			// A blank line inside the block is legal YAML, and comes for
			// free once stripComments has blanked a comment line. Ending
			// the block there silently hid every scope after the first
			// comment — which is how a test asserting a permission was
			// granted passed while reading none of it.
			if strings.TrimSpace(l) == "" {
				continue
			}
			if !strings.HasPrefix(l, " ") {
				break // dedented: the block ended
			}
			k, v, found := strings.Cut(strings.TrimSpace(l), ":")
			if !found {
				continue
			}
			if c := strings.Index(v, "#"); c >= 0 {
				v = v[:c]
			}
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		return out
	}
	return out
}

func rank(level string) int {
	switch level {
	case "write":
		return 2
	case "read":
		return 1
	case "", "none":
		return 0
	}
	panic(fmt.Sprintf("unknown permission level %q", level))
}

// Every plane command builds a snapshot before doing anything, and the
// snapshot reads a fixed set: the agent run list, the open PRs, each
// PR head's CI verdict, and commit ancestry. A workflow that runs the
// binary but names only the scopes its *writes* need gets an HTTP 403
// partway through the claim — after the run looks healthy, and with the
// ticket already dispatched.
// snapshotReads is the read-set of a snapshot build: the scope each
// call needs, spelled as a workflow spells it. Shared by the permission
// test below and the preflight test after it, so "what the sweep needs"
// has one definition rather than two that drift.
var snapshotReads = map[string]string{
	// ListAgentRuns, and ChecksFor — which reads the CI verdict from
	// the Actions API rather than from check-runs. There is no `checks`
	// entry here and there must not be one: a fine-grained token cannot
	// be granted the check-runs API at all, and every agent claim
	// builds a snapshot under exactly such a token (SETUP.md).
	"actions":       "read",
	"pull-requests": "read", // ListOpenPRs
	"contents":      "read", // IsAncestor, via compare
	// State, for projects whose deploy provider is "github" — the
	// dummy's stand-in for a hosting platform. Read lazily, only
	// once a Merged ticket exists, which is why its absence stayed
	// invisible through every run that never reached Merged and then
	// failed every command at once when one did.
	"deployments": "read",
}

func TestWorkflowsRunningThePipelineCanReadTheSnapshot(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	stubs, _ := filepath.Glob(filepath.Join(root, "examples", "stubs", "*.yml"))
	files = append(files, stubs...)

	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		// Two ways a workflow ends up making these calls: it runs the
		// binary itself (the sweep), or it uses one of this repo's
		// composite actions that does. The second is the common case
		// since the agents became actions, and missing it is how this
		// test would quietly stop covering the stubs.
		runsPipeline := strings.Contains(body, "pipeline agent ") || strings.Contains(body, "pipeline sweep")
		// The host is wired only when GITHUB_TOKEN reaches the binary,
		// and without a host the snapshot skips every call above. Not a
		// loophole — verify-live sweeps the tracker alone on purpose —
		// so the read-set is required exactly of the callers that will
		// actually make the calls.
		if runsPipeline && !strings.Contains(body, "GITHUB_TOKEN:") {
			continue
		}
		if !runsPipeline && !usesPipelineAction(t, root, body) {
			continue
		}
		checked++
		have := permissionsOf(body)
		for scope, want := range snapshotReads {
			if rank(have[scope]) < rank(want) {
				t.Errorf("%s runs the pipeline but grants %s %q — the snapshot read will 403 mid-claim",
					filepath.Base(f), scope, have[scope])
			}
		}
	}
	if checked == 0 {
		t.Error("matched no pipeline-running workflows — the detection has drifted")
	}
}

// usesPipelineAction reports whether a workflow calls one of this repo's
// composite actions that runs the pipeline binary. Read from the action
// itself rather than from a list here, so a new action is covered the
// day it is written rather than the day someone remembers this test.
func usesPipelineAction(t *testing.T, root, body string) bool {
	t.Helper()
	uses := regexp.MustCompile(`uses:\s*SwaggerAllen/orchestration/\.github/actions/([\w-]+)@`)
	for _, m := range uses.FindAllStringSubmatch(body, -1) {
		raw, err := os.ReadFile(filepath.Join(root, ".github", "actions", m[1], "action.yml"))
		if err != nil {
			t.Errorf("workflow uses action %q, which is not in this repo: %v", m[1], err)
			continue
		}
		// The same rule the caller applies to a workflow that runs the
		// binary itself: the host, and so the snapshot, is wired only
		// when GITHUB_TOKEN reaches it. The review replay's action runs
		// `pipeline agent record-review` with no token at all — it
		// assembles a prompt from two trees and a config — and asking
		// its stub for pull-requests: read would be asking for a scope
		// no call ever uses.
		if strings.Contains(string(raw), "pipeline agent ") && strings.Contains(string(raw), "GITHUB_TOKEN:") {
			return true
		}
	}
	return false
}

// An action's metadata — everything above `runs:` — is templated, but
// only a few contexts exist there; `github` is not among them. An
// expression in a name, description or default fails the entire file to
// load, before a single step runs, and the error names a line and
// column in a file the caller never opened.
func TestActionMetadataHasNoExpressions(t *testing.T) {
	actions, err := filepath.Glob(filepath.Join("..", "..", ".github", "actions", "*", "action.yml"))
	if err != nil || len(actions) == 0 {
		t.Fatalf("found no actions to check: %v", err)
	}
	for _, a := range actions {
		raw, err := os.ReadFile(a)
		if err != nil {
			t.Fatal(err)
		}
		inOutputs := false
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "runs:") {
				break // steps may use expressions freely
			}
			// `outputs.<id>.value` is the one part of the metadata
			// evaluated after the steps run, so an expression there is
			// not only legal but the only way to write one. Excluded
			// rather than tolerated everywhere, because the failure this
			// test exists for was an expression in an input description.
			if strings.HasPrefix(line, "outputs:") {
				inOutputs = true
				continue
			}
			if inOutputs && line != "" && !strings.HasPrefix(line, " ") {
				inOutputs = false
			}
			if inOutputs {
				continue
			}
			code, _, _ := strings.Cut(line, "#") // comments are not templated
			if strings.Contains(code, "${{") {
				t.Errorf("%s:%d has an expression in its metadata: %s",
					filepath.Base(filepath.Dir(a)), i+1, strings.TrimSpace(line))
			}
		}
	}
}

// Every agent action and the sweep stub used to compile the CLI from
// source before their first tracker call: check out the pipeline,
// install Go, `go run ./cmd/pipeline`. Measured on a real dev run that
// was 32 seconds between the job entering the action and the claim
// landing in Linear. setup-go's own cache cannot help — it keys on
// go.sum and this repo deliberately has no dependencies, so the logs
// said "Primary key was not generated" and every job compiled cold.
//
// The binary is now built once per pipeline commit and cached. A
// `go run` creeping back would be invisible: everything still works,
// just slower every time, on the path that runs hourly.
func TestNothingCompilesThePipelineOnTheHotPath(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	for _, pattern := range []string{
		filepath.Join(root, ".github", "actions", "*", "action.yml"),
		filepath.Join(root, "examples", "stubs", "*.yml"),
	} {
		found, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, found...)
	}
	if len(files) == 0 {
		t.Fatal("found no actions or stubs to check")
	}

	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := stripComments(string(raw))
		// setup-pipeline is where the one build lives, by design.
		if filepath.Base(filepath.Dir(f)) == "setup-pipeline" {
			if !strings.Contains(body, "go -C") {
				t.Error("setup-pipeline no longer builds anything — the other files rely on it")
			}
			continue
		}
		checked++
		if strings.Contains(body, "run ./cmd/pipeline") {
			t.Errorf("%s compiles the pipeline instead of using the cached binary", filepath.Base(f))
		}
	}
	if checked == 0 {
		t.Error("checked nothing — the file globs have drifted")
	}
}

// A file that invokes `pipeline` must be one that put it on PATH, or
// the step dies with "command not found" — an error that says nothing
// about the cause.
func TestEverythingInvokingThePipelineSetsItUpFirst(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	for _, pattern := range []string{
		filepath.Join(root, ".github", "actions", "*", "action.yml"),
		filepath.Join(root, "examples", "stubs", "*.yml"),
	} {
		found, _ := filepath.Glob(pattern)
		files = append(files, found...)
	}
	invokes := regexp.MustCompile(`(?m)^\s*pipeline (agent|sweep|audit|scenario|setup) `)

	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := stripComments(string(raw))
		if !invokes.MatchString(body) {
			continue
		}
		checked++
		// Either it sets the binary up itself, or it calls an action
		// that does — the agent actions nest setup-pipeline so a
		// project's stub needs no change to get the cache.
		setsUp := strings.Contains(body, "actions/setup-pipeline@")
		callsAgentAction := strings.Contains(body, "SwaggerAllen/orchestration/.github/actions/agent-")
		if !setsUp && !callsAgentAction {
			t.Errorf("%s runs the pipeline binary but never puts it on PATH", filepath.Base(f))
		}
	}
	if checked == 0 {
		t.Error("matched nothing that invokes the pipeline — the pattern has drifted")
	}
}

// Reconcile records the stand-in deployment itself, right after the
// merge, because the workflow that used to do it on `push: main` cannot
// fire — GitHub does not start a run from an event created with
// GITHUB_TOKEN, and the merge is made with exactly that. The API call
// needs deployments: write, and a missing scope here surfaces as a 403
// after the PR is already merged, with the ticket mid-transition.
func TestTheReconcileStubCanRecordADeployment(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "stubs", "pipeline-agent-reconcile.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := permissionsOf(stripComments(string(raw)))["deployments"]; rank(got) < rank("write") {
		t.Errorf("the reconcile stub grants deployments %q — the post-deploy check would never get a deployment to read", got)
	}
}

// The event-driven stubs that GitHub's recursion guard makes dead must
// not come back. Each one listened for an event the pipeline itself
// creates with GITHUB_TOKEN, so each fired for a human's activity and
// never for an agent's — the shape that looks healthy and is not.
func TestNoStubWaitsForAnEventTheAgentsCannotCause(t *testing.T) {
	for _, gone := range []string{"pipeline-record-deploy.yml", "pipeline-preview.yml"} {
		path := filepath.Join("..", "..", "examples", "stubs", gone)
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s is back — it triggers on an event GITHUB_TOKEN cannot raise; the work belongs in the harness", gone)
		}
	}
}

// `git fetch <remote> <ref>` names refs as the REMOTE has them, so a
// remote-tracking name like origin/main is never a valid argument —
// it exists only locally. The reconcile action shipped
// `git fetch origin "$BRANCH" origin/main`, which reads as ordinary git
// and dies with "couldn't find remote ref origin/main". It survived
// review and every test until the first time reconcile actually ran,
// at which point it aborted the run and Blocked the ticket.
func TestNoActionFetchesARemoteTrackingName(t *testing.T) {
	actions, err := filepath.Glob(filepath.Join("..", "..", ".github", "actions", "*", "action.yml"))
	if err != nil || len(actions) == 0 {
		t.Fatalf("found no actions to check: %v", err)
	}
	stubs, _ := filepath.Glob(filepath.Join("..", "..", "examples", "stubs", "*.yml"))
	// A fetch argument of the form origin/x, but not inside a refspec
	// (which legitimately contains refs/remotes/origin/...).
	fetches := regexp.MustCompile(`git fetch [^\n]*?(^|\s)origin/\S+`)
	for _, f := range append(actions, stubs...) {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// Join backslash continuations first. The fix for this very bug
		// spread the fetch over several lines, and a line-at-a-time scan
		// would have stopped seeing the arguments it exists to check.
		joined := strings.ReplaceAll(stripComments(string(raw)), "\\\n", " ")
		for i, line := range strings.Split(joined, "\n") {
			if !strings.Contains(line, "git fetch ") {
				continue
			}
			if fetches.MatchString(line) {
				t.Errorf("%s:%d fetches a remote-tracking name, which exists on no remote: %s",
					filepath.Base(filepath.Dir(f)), i+1, strings.TrimSpace(line))
			}
		}
	}
}

// Every agent's model pass goes through run-agent-model, which is the
// only place that decides which credential pays for it. An action that
// shells out to the CLI directly would silently opt that agent out of
// the subscription-first ordering — and out of the failover, so an
// exhausted allowance would abort the ticket rather than cost a retry.
func TestEveryAgentActionRunsTheModelThroughTheSharedRunner(t *testing.T) {
	actions, err := filepath.Glob(filepath.Join("..", "..", ".github", "actions", "agent-*", "action.yml"))
	if err != nil || len(actions) == 0 {
		t.Fatalf("found no agent actions to check: %v", err)
	}
	for _, a := range actions {
		raw, err := os.ReadFile(a)
		if err != nil {
			t.Fatal(err)
		}
		body := stripComments(string(raw))
		name := filepath.Base(filepath.Dir(a))
		if !strings.Contains(body, "actions/run-agent-model@") {
			t.Errorf("%s never calls run-agent-model — its model pass has its own credential policy", name)
		}
		if strings.Contains(body, "@anthropic-ai/claude-code") {
			t.Errorf("%s invokes the CLI itself; the credential choice belongs in run-agent-model", name)
		}
	}
}

// Claude Code resolves credentials in a fixed order and ANTHROPIC_API_KEY
// beats the subscription token, so a step holding both in its own env
// runs every "subscription" pass on the API key and reports success.
// The failover would be untestable and the bill would be the only tell.
// run-agent-model therefore carries the secrets under neutral names and
// exports the real one per attempt, on the child process.
func TestTheModelRunnerNeverShadowsTheSubscriptionCredential(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "actions", "run-agent-model", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(raw))
	for _, line := range strings.Split(body, "\n") {
		// A step-level `env:` entry is `NAME: <value>` at indent; the
		// per-attempt exports are `env -u X NAME="$VAR"` inside run:.
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "env -u ") {
			continue
		}
		for _, read := range []string{"ANTHROPIC_API_KEY:", "CLAUDE_CODE_OAUTH_TOKEN:"} {
			if strings.HasPrefix(trimmed, read) {
				t.Errorf("run-agent-model puts %s in a step env: %s", strings.TrimSuffix(read, ":"), trimmed)
			}
		}
	}
	// And each attempt must unset the other, not merely set its own.
	for _, want := range []string{
		`env -u ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN=`,
		`env -u CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_API_KEY=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("run-agent-model has no attempt of the form %q — the unset is what stops the shadowing", want)
		}
	}
}

// packageSource is every non-test source file of this package, joined.
// The checks these tests read are assembled in preflight.go but their
// labels need not be declared there — deployCredential moved to main.go
// when preflight stopped keeping its own copy of the provider switch, and
// a grep of one file reported that preflight had stopped probing the
// deployments scope when nothing about the probing had changed. A guard
// that names a file asserts where a string lives; what these want to know
// is whether the package says it at all.
func packageSource(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("found no package sources: %v", err)
	}
	var b strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	return b.String()
}

// Preflight has to probe every scope the snapshot needs, or it is a
// green light that means nothing. Read off the check list's own scope
// strings, so adding a call to the read-set without adding a probe for
// it fails here rather than at the next 403.
func TestPreflightProbesEveryScopeTheSnapshotNeeds(t *testing.T) {
	src := packageSource(t)
	for scope := range snapshotReads {
		// The scope as a workflow spells it, which is how the check list
		// prints it too — the string a reader has to go edit.
		if !strings.Contains(src, `"`+scope+`: read"`) {
			t.Errorf("preflight probes nothing needing %q — a green preflight would still be followed by a 403 on that call", scope)
		}
	}
}

// A green preflight must not read as "the permissions are fine". It
// probes reads; every write the pipeline makes has a side effect
// somebody would have to undo, so none of them are probed — and the most
// expensive 403 of the lot was reconcile's POST /deployments, a write.
// The unexercised list is what keeps the green line honest, so it has to
// name every write scope the stubs actually grant.
func TestPreflightNamesTheWritesItDoesNotProbe(t *testing.T) {
	body, err := os.ReadFile("preflight.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	stubs, err := filepath.Glob(filepath.Join("..", "..", "examples", "stubs", "*.yml"))
	if err != nil || len(stubs) == 0 {
		t.Fatalf("found no stubs: %v", err)
	}
	granted := map[string]bool{}
	for _, f := range stubs {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for scope, level := range permissionsOf(stripComments(string(raw))) {
			if level == "write" {
				granted[scope] = true
			}
		}
	}
	if len(granted) == 0 {
		t.Fatal("no stub grants any write — the scan has drifted")
	}
	for scope := range granted {
		if !strings.Contains(src, scope+": write") {
			t.Errorf("a stub grants %s: write and preflight neither probes it nor names it as unexercised — a green run would imply it was checked", scope)
		}
	}
}

// Every stub that runs the project's own commands must carry the same
// environment block, byte for byte.
//
// They drifted into saying "toolchain", which is true and not enough:
// an agent runs the config's quality gates before finishing and the live
// suite runs the project's test command, so the job needs whatever those
// need. Two runs were lost to the narrower reading — `mix test` with no
// database CI provides, and a live suite that could not start because
// nothing had installed dependencies — and in both the toolchain was
// correct. Identical text is the cheap way to keep one lesson from
// landing in one stub and not the other four.
func TestStubsShareOneEnvironmentBlock(t *testing.T) {
	stubs, err := filepath.Glob(filepath.Join("..", "..", "examples", "stubs", "pipeline-agent-*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join("..", "..", "examples", "stubs", "pipeline-live-suite.yml")
	stubs = append(stubs, live)

	const open = "# ---- this project's environment, before the pipeline runs"
	blocks := map[string][]string{}
	for _, f := range stubs {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		start := -1
		for i, l := range lines {
			if strings.Contains(l, open) {
				start = i
				break
			}
		}
		if start < 0 {
			t.Errorf("%s has no environment block — it runs the project's commands and must say what that needs", filepath.Base(f))
			continue
		}
		end := start
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "# ------") || end == start {
			end++
		}
		blocks[filepath.Base(f)] = lines[start : end+1]
	}
	if len(blocks) < 2 {
		t.Fatal("found fewer than two stubs to compare; the glob has drifted")
	}
	var first string
	var firstName string
	for name, b := range blocks {
		joined := strings.Join(b, "\n")
		if first == "" {
			first, firstName = joined, name
			continue
		}
		if joined != first {
			t.Errorf("%s's environment block differs from %s's; one of them is missing a lesson the other learned", name, firstName)
		}
	}
}

// The kill switch has to stop the job, not only the planning inside it.
//
// The binary refuses when the flag is set, which is correct and is not
// free: Actions bills each job rounded up to the minute, so a parked
// project on the hourly beat spends ~730 minutes a month producing
// nothing. A skipped job costs nothing. Without the condition, "parked"
// still bills, and the rehearsal repo quietly consumes the allowance the
// live project needs.
func TestTheSweepStubSkipsItselfWhenKilled(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "stubs", "pipeline-sweep.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := stripComments(string(raw))
	if !strings.Contains(body, "if: vars.PIPELINE_KILL_SWITCH != 'true'") {
		t.Error("the sweep job has no kill-switch condition — a parked project still pays for every beat")
	}
	// And the binary keeps its own refusal, so a hand-dispatched run with
	// the flag set does not sail past the guard the workflow provides.
	if !strings.Contains(body, "PIPELINE_KILL_SWITCH: ${{ vars.PIPELINE_KILL_SWITCH }}") {
		t.Error("the flag no longer reaches the binary; the workflow would be the only guard")
	}
}
