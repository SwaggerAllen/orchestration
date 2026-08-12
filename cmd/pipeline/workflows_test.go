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
