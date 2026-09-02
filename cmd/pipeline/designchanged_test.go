package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/filemap"
)

// authoredCommand lifts the pass's changed-file command out of the
// design action and makes it runnable.
//
// Read from the shipped file rather than restated here, so editing the
// action is what this test reads. A copy would let the action drift and
// keep printing ok — the failure this repo already records for a probe
// that never ran.
func authoredCommand(t *testing.T, base string) string {
	t.Helper()
	body := repoFile(t, filepath.Join(".github", "actions", "agent-design", "action.yml"))
	// Anchored on the file the command writes, then walked back over
	// the pipeline's continuation lines — not on any flag it uses. The
	// flags are what this test exists to judge, so an anchor naming one
	// turns removing it into an extraction failure, which proves only
	// that the test mentions it. Anchoring on `git log` instead was no
	// better: the action runs one earlier, for the prompt.
	all := strings.Split(body, "\n")
	end := -1
	for i, line := range all {
		if strings.Contains(line, `> "$RUNNER_TEMP/pipeline/changed.txt"`) {
			end = i
		}
	}
	if end < 0 {
		t.Fatal("the design action no longer writes the pass's files to changed.txt")
	}
	start := end
	for start > 0 && strings.HasSuffix(strings.TrimSpace(all[start-1]), `\`) {
		start--
	}
	var lines []string
	for _, line := range all[start : end+1] {
		lines = append(lines, strings.TrimSuffix(strings.TrimSpace(line), `\`))
	}
	cmd := strings.Join(lines, " ")
	cmd = regexp.MustCompile(`\$\{\{[^}]*\}\}`).ReplaceAllString(cmd, base)
	cmd = strings.ReplaceAll(cmd, `"$RUNNER_TEMP/pipeline/changed.txt"`, "/dev/stdout")
	return cmd
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A repository shaped like Catapult's ORC-116: a design branch whose
// base moved, which merged origin/main and then did its own work.
func orc116(t *testing.T, hidden string) (dir, base string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "systems/dashboard.md", "start\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")

	git(t, dir, "checkout", "-qb", "design")
	base = git(t, dir, "rev-parse", "HEAD")

	// main moves under the pass: another ticket's Elixir lands there.
	git(t, dir, "checkout", "-q", "main")
	write(t, dir, "lib/catapult/dsl/workflow.ex", "defmodule W do\nend\n")
	write(t, dir, "test/support/dsl_fixture.ex", "defmodule F do\nend\n")
	write(t, dir, "systems/dashboard.md", "main's edit\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "another ticket's implementation")

	// The pass does the correct thing: merge the base it was drawn
	// against, resolving the one conflict, then write its own work.
	//
	// Both sides edit systems/dashboard.md so the merge genuinely
	// conflicts. An earlier version of this fixture had only main touch
	// it: the merge fast-forwarded that file, no resolution happened,
	// and the conflict half of this test was asserting against a
	// scenario it had not built. So the merge is required to fail here
	// — a fixture that silently sets up the easy case is the probe that
	// never ran.
	git(t, dir, "checkout", "-q", "design")
	write(t, dir, "screens/board.md", "the pass's screen\n")
	write(t, dir, "systems/dashboard.md", "the pass's edit\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "design work before the merge")
	if out, err := exec.Command("git", "-C", dir, "merge", "main", "-m", "Merge main").CombinedOutput(); err == nil {
		t.Fatalf("the fixture's merge did not conflict, so it never built the case under test:\n%s", out)
	}
	write(t, dir, "systems/dashboard.md", "resolved\n")
	if hidden != "" {
		// Written *in the merge commit itself*, which no parent
		// carries. Resolving a conflict is an edit like any other, and
		// nothing stops a pass making others while it is there.
		write(t, dir, hidden, "defmodule Hidden do\nend\n")
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "Merge main")
	write(t, dir, "screens/ticket.md", "more of the pass's own work\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "the pass's own work")
	return dir, base
}

func authored(t *testing.T, dir, base string) []string {
	t.Helper()
	c := exec.Command("bash", "-c", authoredCommand(t, base))
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("running the action's command: %v\n%s", err, out)
	}
	return strings.Fields(string(out))
}

var owned = []string{"screens/**", "storybook/**", "systems/*.md", "docs/*.md"}

// The first half: a pass whose base moved merges origin/main, and the
// files that merge brought in are not the pass's to answer for.
//
// This cost a full re-dispatch on ORC-116. The gate reported 23 files
// "outside the paths design owns" — ten `lib/**.ex` among them — on a
// pass whose own commit touched ten files, all of them owned. The
// report grows with how far main moved, which makes it read as a worse
// violation the more wrong it is.
func TestAMergedBaseIsNotTheDesignPassesDoing(t *testing.T) {
	dir, base := orc116(t, "")
	got := authored(t, dir, base)
	for _, f := range got {
		if strings.HasPrefix(f, "lib/") || strings.HasPrefix(f, "test/") {
			t.Errorf("a file the merge brought in is billed to the pass: %s (all: %v)", f, got)
		}
	}
	if strays := filemap.DesignAudit(owned, got); len(strays) != 0 {
		t.Errorf("a pass that merely merged its base failed the ownership gate:\n%s", strays[0])
	}
}

// The second half, and the one a plainer "skip merge commits" quietly
// breaks: what a merge commit *itself* writes is the merger's own doing.
// Here the pass writes an implementation file while resolving the
// conflict — no parent carries it, so skipping merge commits makes it
// invisible and the gate waves through exactly what it exists to catch.
//
// An earlier version asserted on the conflicted file instead, and that
// proved nothing: the pass's own pre-merge commit had touched it too, so
// the assertion passed with merge commits skipped entirely.
func TestAnImplementationHiddenInTheMergeCommitIsStillCaught(t *testing.T) {
	dir, base := orc116(t, "lib/catapult/hidden.ex")
	got := authored(t, dir, base)
	strays := filemap.DesignAudit(owned, got)
	if len(strays) == 0 {
		t.Fatalf("a file written inside the merge commit escaped the gate: %v", got)
	}
	if !strings.Contains(strays[0], "lib/catapult/hidden.ex") {
		t.Errorf("the finding does not name it: %s", strays[0])
	}
}

// And the gate must still catch the thing it exists for: a pass that
// writes the implementation itself.
func TestAPassThatWritesImplementationStillFails(t *testing.T) {
	dir, base := orc116(t, "")
	write(t, dir, "lib/catapult/dsl/status.ex", "defmodule S do\nend\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "the pass writes the implementation")

	got := authored(t, dir, base)
	strays := filemap.DesignAudit(owned, got)
	if len(strays) == 0 {
		t.Fatalf("a pass that wrote lib/**.ex passed the ownership gate: %v", got)
	}
	if !strings.Contains(strays[0], "lib/catapult/dsl/status.ex") {
		t.Errorf("the finding does not name the file the pass wrote: %s", strays[0])
	}
}

// recordCommand lifts the record review's extraction command out of the
// design action, the way authoredCommand lifts changed.txt's: anchored
// on the file it writes, walked back over the continuation lines,
// never on a flag — the flags are what the test judges.
func recordCommand(t *testing.T, base string) string {
	t.Helper()
	body := repoFile(t, filepath.Join(".github", "actions", "agent-design", "action.yml"))
	all := strings.Split(body, "\n")
	end := -1
	for i, line := range all {
		if strings.Contains(line, `> "$RUNNER_TEMP/pipeline/record-diff.patch"`) {
			end = i
		}
	}
	if end < 0 {
		t.Fatal("the design action no longer writes the pass's record diff to record-diff.patch")
	}
	start := end
	for start > 0 && strings.HasSuffix(strings.TrimSpace(all[start-1]), `\`) {
		start--
	}
	var lines []string
	for _, line := range all[start : end+1] {
		lines = append(lines, strings.TrimSuffix(strings.TrimSpace(line), `\`))
	}
	cmd := strings.Join(lines, " ")
	cmd = regexp.MustCompile(`\$\{\{[^}]*\}\}`).ReplaceAllString(cmd, base)
	cmd = strings.ReplaceAll(cmd, `"$RUNNER_TEMP/pipeline/record-diff.patch"`, "/dev/stdout")
	return cmd
}

// The record review reads what the pass wrote, not what its merge
// brought in — the same first-parent discipline as changed.txt, one
// level up, and asserted against the same repo shape. A range diff
// would hand the reviewer main's doc edits as this pass's writing and
// decline the pass for narration somebody else wrote.
func TestTheRecordDiffIsThePassesOwnMarkdownAndNotItsMerges(t *testing.T) {
	dir, base := orc116(t, "lib/catapult/hidden.ex")
	c := exec.Command("bash", "-c", recordCommand(t, base))
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("running the action's command: %v\n%s", err, out)
	}
	got := string(out)
	// The pass's own writing, in its own commits and in the merge
	// commit's resolution, is all there.
	for _, want := range []string{"+the pass's screen", "+the pass's edit", "+more of the pass's own work", "+resolved"} {
		if !strings.Contains(got, want) {
			t.Errorf("the pass's own line %q is missing from the record diff:\n%s", want, got)
		}
	}
	// Main's edit to the same file arrived through the merge and is
	// not the pass's writing. It may appear as a removed line — the
	// resolution replaced it — never as an addition.
	if strings.Contains(got, "+main's edit") {
		t.Errorf("a doc edit the merge brought in from main is billed to the pass as its own writing:\n%s", got)
	}
	// Markdown only: the implementation hidden in the merge commit is
	// the ownership audit's to catch, not the record review's to read.
	if strings.Contains(got, "hidden.ex") || strings.Contains(got, "workflow.ex") {
		t.Errorf("the record diff carries non-markdown:\n%s", got)
	}
}
