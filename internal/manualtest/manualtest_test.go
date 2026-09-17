package manualtest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/filemap"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
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

func TestLoadReadsCoversAndSkipsNonTests(t *testing.T) {
	dir := t.TempDir()
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
	if got[0].Path != filepath.ToSlash(filepath.Join(dir, "queue-drains.md")) {
		t.Errorf("path = %q", got[0].Path)
	}
}

// A reasons file read as a test is a test that can never pass and whose
// id nobody recognises — and it would be reported as covering nothing,
// which is a finding about a file that is not a test at all.
func TestLoadSkipsTheReasonsSibling(t *testing.T) {
	dir := t.TempDir()
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
	systems := []filemap.Doc{{Name: "engine"}, {Name: "delivery"}}
	screens := []filemap.Doc{{Name: "board"}}
	tests := []Test{
		{ID: "ok", Path: "tests/manual/ok.md", Covers: []string{"system:engine", "screen:board"}},
		{ID: "typo", Path: "tests/manual/typo.md", Covers: []string{"system:engnie"}},
		{ID: "renamed", Path: "tests/manual/renamed.md", Covers: []string{"screen:kanban"}},
	}
	got := Problems(tests, systems, screens)
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
	got := Problems([]Test{{ID: "x", Path: "tests/manual/x.md"}}, nil, nil)
	if len(got) != 1 || !contains(got[0], "covers nothing") {
		t.Errorf("problems = %v", got)
	}
}

// A system and a screen may share a name; the prefix is what tells them
// apart, and dropping it would let a test claiming one be validated by
// the other.
func TestProblemsKeepsSystemAndScreenNamespacesApart(t *testing.T) {
	systems := []filemap.Doc{{Name: "board"}}
	tests := []Test{{ID: "x", Path: "tests/manual/x.md", Covers: []string{"screen:board"}}}
	if got := Problems(tests, systems, nil); len(got) != 1 {
		t.Errorf("a screen seam was validated by a system doc of the same name: %v", got)
	}
}

func TestProblemsIsEmptyOnACoherentSet(t *testing.T) {
	systems := []filemap.Doc{{Name: "engine"}}
	screens := []filemap.Doc{{Name: "board"}}
	tests := []Test{{ID: "ok", Path: "tests/manual/ok.md", Covers: []string{"system:engine", "screen:board"}}}
	if got := Problems(tests, systems, screens); len(got) != 0 {
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
