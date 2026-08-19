package main

import (
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
