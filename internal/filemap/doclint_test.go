package filemap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintDocFlagsStateAndInventorySections(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"states", "# home\n\n## States\n\n- empty\n- loaded\n", "state sections"},
		{"variations", "# home\n\n### Variations\n\n- default\n", "state sections"},
		{"singular state", "# home\n\n## State\n", "state sections"},
		{"modules", "# billing\n\n## Modules\n\n- `App.Billing`\n", "code inventory"},
		{"functions", "# billing\n\n## Functions\n", "code inventory"},
		{"api", "# billing\n\n## API\n", "code inventory"},
		{"bold heading", "# home\n\n## **States**\n", "state sections"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LintDoc("screens/home.md", tc.doc)
			if len(got) != 1 {
				t.Fatalf("violations = %v, want exactly one", got)
			}
			if !strings.Contains(got[0], tc.want) {
				t.Errorf("violation %q does not name %q", got[0], tc.want)
			}
			if !strings.Contains(got[0], "screens/home.md") {
				t.Errorf("violation %q does not name the file", got[0])
			}
		})
	}
}

// The docs this repo's own agents write must pass. A lint that fails
// good work is one someone switches off, and then the rule it enforced
// is gone along with it — worse than never having had the check.
func TestLintDocPassesTheDocsWeActuallyWrite(t *testing.T) {
	good := []string{
		// The dummy's real screen doc, near enough: prose that discusses
		// states without sectioning them.
		"---\nfiles:\n  - lib/dummy_web/components/home.ex\n---\n\n# home\n\n" +
			"## Standing decisions\n\n- The two lines always appear together. The farewell is not\n" +
			"  an optional embellishment; a hero with one line is a bug, which is why no\n" +
			"  variation shows one.\n- No state sections here: the state list lives in the\n" +
			"  stories alone (pipeline DESIGN §4).\n",
		// A standing decision naming a function is the decision doing
		// its job, not an inventory.
		"# greetings\n\n## Standing decisions\n\n- Wording the farewell is this system's job, so\n" +
			"  `farewell/1` lives here alongside the greeting.\n",
		// A heading that merely contains a banned word.
		"# home\n\n## Standing decisions about state\n\nProse.\n",
		"# billing\n\n## Why the API boundary sits here\n\nProse.\n",
	}
	for i, doc := range good {
		if got := LintDoc("systems/x.md", doc); len(got) != 0 {
			t.Errorf("case %d flagged a good doc: %v", i, got)
		}
	}
}

// A doc explaining the format may show a banned heading inside a fence.
// The bootstrap prompt's output does exactly this.
func TestLintDocIgnoresFencedExamples(t *testing.T) {
	doc := "# format\n\nA doc must not do this:\n\n```markdown\n## States\n\n- empty\n```\n\nThat is all.\n"
	if got := LintDoc("systems/x.md", doc); len(got) != 0 {
		t.Errorf("flagged a fenced example: %v", got)
	}
}

func TestLintDirSkipsReadmeAndMissingDirs(t *testing.T) {
	dir := t.TempDir()
	// README.md is prose about the directory, not a doc — LoadDir
	// excepts it and the lint must agree, or systems/README.md's own
	// prose starts failing builds.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Systems\n\n## Files\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "billing.md"), []byte("# billing\n\n## States\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "billing.md") {
		t.Errorf("violations = %v, want only billing.md's", got)
	}

	if got, err := LintDir(filepath.Join(dir, "nope")); err != nil || got != nil {
		t.Errorf("missing dir = %v, %v; want nothing, nil", got, err)
	}
}

// The normaliser trims `#` from both ends of a heading, so before the id
// token was stripped first, `## #3 States` normalised to "3 states" and
// passed — the token meant to be inert disarmed the lint on the docs
// carrying it. Both directions: the banned heading is still caught
// through its token, and a heading that merely contains a banned word
// still is not.
func TestLintDocSeesThroughARuleId(t *testing.T) {
	if got := LintDoc("systems/x.md", "# x\n\n## #3 States\n"); len(got) != 1 || !strings.Contains(got[0], `"#3 States"`) {
		t.Errorf("violations = %v, want the heading flagged through its id", got)
	}
	if got := LintDoc("systems/x.md", "# x\n\n## #3 Standing decisions about state\n"); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
}

// An entry's prose may quote the banned heading it explains, and a
// reasons file's own h2s are ids. It is not a doc and is not linted.
func TestLintDirSkipsReasonsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "billing.reasons.md"), []byte("## #4\n\n## Files\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "billing.md"), []byte("# billing\n\n## #1 Standing decisions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
}
