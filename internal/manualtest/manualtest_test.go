package manualtest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/filemap"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// tree returns a project root with an empty tests/manual/ in it, since
// Load takes the root and derives Dir itself.
func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, Dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func covering(seams ...string) string {
	s := "---\ncovers:\n"
	for _, x := range seams {
		s += "  - " + x + "\n"
	}
	return s + "---\n\n# A manual test\n"
}

func ids(tests []Test) []string {
	out := []string{}
	for _, t := range tests {
		out = append(out, t.ID)
	}
	return out
}

// recordsOf builds a Records set from doc names per record kind, keyed by
// the kind's directory.
//
// Built over protocol.RecordKinds rather than from two literals, so a
// fourth record kind arrives in these fixtures by existing rather than by
// somebody remembering this file. The pair-of-literals version is exactly
// what made Problems reject a `dsl:` seam the day the third kind landed.
func recordsOf(byDir map[string][]string) filemap.Records {
	var r filemap.Records
	for _, k := range protocol.RecordKinds {
		var docs []filemap.Doc
		for _, name := range byDir[k.Dir] {
			docs = append(docs, filemap.Doc{Name: name})
		}
		r.Kinds = append(r.Kinds, filemap.KindDocs{Kind: k, Docs: docs})
	}
	return r
}

func TestLoadReadsCoversAndSkipsNonTests(t *testing.T) {
	dir := tree(t)
	write(t, dir, "queue-drains.md", covering("system:delivery", "screen:board"))
	write(t, dir, "queue-drains.reasons.md", "## #1\n\nwhy\n")
	write(t, dir, "README.md", "not a test\n")
	write(t, dir, "notes.txt", "not markdown\n")

	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(got), []string{"queue-drains"}) {
		t.Fatalf("ids = %v", ids(got))
	}
	if !reflect.DeepEqual(got[0].Covers, []string{"system:delivery", "screen:board"}) {
		t.Errorf("covers = %v", got[0].Covers)
	}
	if got[0].Path != "tests/manual/queue-drains.md" {
		t.Errorf("path = %q", got[0].Path)
	}
}

// A reasons file read as a test is a test that can never pass and whose
// id nobody recognises — and it would be reported as covering nothing,
// which is a finding about a file that is not a test at all.
func TestLoadSkipsTheReasonsSibling(t *testing.T) {
	dir := tree(t)
	write(t, dir, "a.reasons.md", covering("system:engine"))
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("loaded %v", ids(got))
	}
}

// A project with no manual tests yet is legal: the gate has nothing to
// run rather than something to complain about.
func TestLoadMissingDirIsNotAnError(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestSelectTakesOnlyTestsWhoseSeamTheDiffTouches(t *testing.T) {
	tests := []Test{
		{ID: "a", Covers: []string{"system:engine"}},
		{ID: "b", Covers: []string{"screen:board"}},
		{ID: "c", Covers: []string{"system:delivery", "screen:board"}},
		{ID: "d", Covers: []string{"system:foundation"}},
	}
	got := Select(tests, []string{"screen:board"})
	if !reflect.DeepEqual(ids(got), []string{"b", "c"}) {
		t.Errorf("ids = %v, want b and c", ids(got))
	}
}

// One seam in common is enough. A test covering a crossing is exactly
// the test a diff touching either side should run.
func TestSelectTakesATestWhenAnyOneSeamMatches(t *testing.T) {
	tests := []Test{{ID: "crossing", Covers: []string{"system:engine", "system:delivery"}}}
	for _, seam := range []string{"system:engine", "system:delivery"} {
		if got := Select(tests, []string{seam}); len(got) != 1 {
			t.Errorf("%s selected %v", seam, ids(got))
		}
	}
}

func TestSelectTakesNothingWhenTheDiffTouchesNoCoveredSeam(t *testing.T) {
	tests := []Test{{ID: "a", Covers: []string{"system:engine"}}}
	if got := Select(tests, []string{"system:billing"}); len(got) != 0 {
		t.Errorf("selected %v", ids(got))
	}
}

// The gate on the gate. §8.2's argument for letting tests/manual/**
// accumulate is that a manual test is executed, so it cannot rot
// unnoticed — which holds only while every test is reachable. A test
// naming a seam no doc declares is never selected and never fails, so it
// is an unchecked claim again, which is the thing §1 refused.
func TestProblemsReportsATestNoDiffCanEverSelect(t *testing.T) {
	records := recordsOf(map[string][]string{
		"systems": {"engine", "delivery"},
		"screens": {"board"},
	})
	tests := []Test{
		{ID: "ok", Path: "tests/manual/ok.md", Covers: []string{"system:engine", "screen:board"}},
		{ID: "typo", Path: "tests/manual/typo.md", Covers: []string{"system:engnie"}},
		{ID: "renamed", Path: "tests/manual/renamed.md", Covers: []string{"screen:kanban"}},
	}
	got := Problems(tests, records)
	if len(got) != 2 {
		t.Fatalf("problems = %v, want the two unreachable ones", got)
	}
	for _, want := range []string{"typo.md", "engnie", "renamed.md", "kanban"} {
		found := false
		for _, g := range got {
			if contains(g, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no problem names %q: %v", want, got)
		}
	}
}

// The same defect stated more plainly.
func TestProblemsReportsATestThatCoversNothing(t *testing.T) {
	got := Problems([]Test{{ID: "x", Path: "tests/manual/x.md"}}, recordsOf(nil))
	if len(got) != 1 || !contains(got[0], "covers nothing") {
		t.Errorf("problems = %v", got)
	}
}

// A system and a screen may share a name; the prefix is what tells them
// apart, and dropping it would let a test claiming one be validated by
// the other.
func TestProblemsKeepsRecordKindNamespacesApart(t *testing.T) {
	records := recordsOf(map[string][]string{"systems": {"board"}})
	tests := []Test{{ID: "x", Path: "tests/manual/x.md", Covers: []string{"screen:board"}}}
	if got := Problems(tests, records); len(got) != 1 {
		t.Errorf("a screen seam was validated by a system doc of the same name: %v", got)
	}
}

// **Every record kind, not the two this package used to name.** Written
// against `system:` and `screen:` as literals, Problems reported a test
// covering `dsl:` as covering a seam no doc declares — refusing a test
// for naming the newest record kind, and the refusal looked exactly like
// the typo it is meant to catch.
//
// Driven off protocol.RecordKinds so a fourth kind is covered by
// existing, which is the only version of this that stays true.
func TestProblemsAcceptsASeamOfEveryRecordKind(t *testing.T) {
	byDir := map[string][]string{}
	var tests []Test
	for _, k := range protocol.RecordKinds {
		byDir[k.Dir] = []string{"thing"}
		tests = append(tests, Test{
			ID:     k.Cite,
			Path:   "tests/manual/" + k.Cite + ".md",
			Covers: []string{k.LabelPrefix + "thing"},
		})
	}
	if len(tests) < 3 {
		t.Fatalf("only %d record kinds; this test asserts the table is read rather than a pair", len(tests))
	}
	if got := Problems(tests, recordsOf(byDir)); len(got) != 0 {
		t.Errorf("a seam of a declared record kind was reported unreachable: %v", got)
	}
}

// And the selection half: a diff touching a dsl doc's paths selects the
// test covering it. Problems accepting the seam is worth nothing if
// nothing ever matches it.
func TestSelectTakesADslSeam(t *testing.T) {
	k, ok := protocol.RecordKindByDir("docs/dsl")
	if !ok {
		t.Skip("no docs/dsl record kind in this protocol")
	}
	tests := []Test{{ID: "grammar", Covers: []string{k.LabelPrefix + "chain"}}}
	if got := Select(tests, []string{k.LabelPrefix + "chain"}); len(got) != 1 {
		t.Errorf("a dsl seam selected %v", ids(got))
	}
}

func TestProblemsIsEmptyOnACoherentSet(t *testing.T) {
	records := recordsOf(map[string][]string{"systems": {"engine"}, "screens": {"board"}})
	tests := []Test{{ID: "ok", Path: "tests/manual/ok.md", Covers: []string{"system:engine", "screen:board"}}}
	if got := Problems(tests, records); len(got) != 0 {
		t.Errorf("problems = %v", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
