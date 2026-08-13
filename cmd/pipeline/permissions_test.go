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
			if strings.TrimSpace(l) == "" || !strings.HasPrefix(l, " ") {
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
func TestWorkflowsRunningThePipelineCanReadTheSnapshot(t *testing.T) {
	snapshotReads := map[string]string{
		"actions":       "read", // ListAgentRuns
		"pull-requests": "read", // ListOpenPRs
		"checks":        "read", // ChecksFor
		"contents":      "read", // IsAncestor, via compare
	}
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
		if strings.Contains(string(raw), "pipeline agent ") {
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
