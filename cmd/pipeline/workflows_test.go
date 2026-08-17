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
