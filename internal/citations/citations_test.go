package citations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func world(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "docs/v5-design-decisions.md", strings.Join([]string{
		"# Design decisions",
		"## 7. Machinery",
		"### 7.8 Containers, queues, and milestones",
		"### 7.16 The work surface",
	}, "\n")+"\n")
	return root
}

// The check earns its place on this case and no other: a citation that
// resolved when it was written, against a section a later pass removed.
// Nothing is wrong at the moment of writing, so no prompt rule reaches
// it and only a sweep finds it.
func TestADanglingSectionIsReported(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# See docs/v5-design-decisions.md §7.19 for the reopen mechanism\n")

	got, err := Sweep(root, []string{"lib/thing.ex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Sweep = %v, want the dangling §7.19", got)
	}
	if !strings.Contains(got[0].String(), "§7.19") || !strings.Contains(got[0].String(), "lib/thing.ex:1") {
		t.Errorf("the report does not say where or what: %s", got[0])
	}
}

// It must sweep the whole tree, which is the finding ORC-143 says
// matters most. Scoped to docs/ and systems/, Catapult's own audit
// reported clean while 22 citations in lib/, components/ and test/ were
// dangling — two of them inside error message strings, so the audit was
// telling developers to go read an entry that no longer existed.
func TestItResolvesCitationsOutsideTheDocsTree(t *testing.T) {
	root := world(t)
	write(t, root, "lib/audit.ex", `raise "see docs/v5-design-decisions.md §9.9"`+"\n")
	write(t, root, "bundles/default/tiers/impl.yaml", "prompt: docs/v5-design-decisions.md §9.9\n")
	write(t, root, "test/support/case.ex", "# docs/v5-design-decisions.md §7.8 is fine\n")

	got, err := Sweep(root, []string{"lib/audit.ex", "bundles/default/tiers/impl.yaml", "test/support/case.ex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Sweep = %v, want both non-docs citations reported and the live one silent", got)
	}
}

// A citation naming a document that does not exist is the same defect as
// one naming a section that does not, and must not pass quietly.
func TestACitationOfAMissingDocumentIsReported(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# docs/gone.md §1.1\n")

	got, err := Sweep(root, []string{"lib/thing.ex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Why, "does not have") {
		t.Fatalf("Sweep = %v, want the missing document reported", got)
	}
}

// A parent section is satisfied by its children existing. Documents
// number differently and a citation of §7 is not a defect because the
// file only writes `### 7.8`.
func TestAParentSectionResolvesThroughItsChildren(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# docs/v5-design-decisions.md §7 and §7.8\n")

	got, err := Sweep(root, []string{"lib/thing.ex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none", got)
	}
}

// The separator is narrow on purpose. A permissive gap pairs a document
// with a section number from the next sentence, and a false failure on a
// gate is how suppressions get added — the failure mix.exs's own
// ignore_advisories reasoning already names.
func TestItDoesNotPairADocumentWithADistantSectionNumber(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex",
		"# docs/v5-design-decisions.md is the source of truth. Elsewhere, §9.9 of something else applies.\n")

	got, err := Sweep(root, []string{"lib/thing.ex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none — §9.9 belongs to another sentence", got)
	}
}

// Shorthand citations are the majority form and are deliberately not
// resolved: `v5 §7.8` needs a project-declared map, which is an
// author-owned pipeline.config.json edit that lands first. Asserted so
// that "not covered" stays a decision rather than becoming a silent hole
// somebody later reads as coverage.
func TestAShorthandCitationIsNotResolvedYet(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# v5 §9.9 and conventions §2 are not checked here\n")

	got, err := Sweep(root, []string{"lib/thing.ex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none — shorthand needs a declared map first", got)
	}
}

// A path whose last segment is `docs/` must not be read as `docs/`.
//
// Not hypothetical: anchored on `docs/`, the first run of this package
// against Catapult's tree reported two dangling citations that were both
// correct — `seed-docs/catapult-default-bundle-v4.md §2.3` and its
// companion. The pattern matched the substring and looked for a file
// that name never referred to. Two false failures on a gate, found only
// because the package was run against a real corpus before it shipped.
func TestASeedDocsPathIsNotReadAsDocs(t *testing.T) {
	root := world(t)
	write(t, root, "seed-docs/v4.md", "## 2. Vocabulary\n### 2.3 Tiers\n")
	write(t, root, "systems/thing.md", "See `seed-docs/v4.md` §2.3 for the v4 shape.\n")

	got, err := Sweep(root, []string{"systems/thing.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none — the citation names seed-docs/v4.md and it resolves", got)
	}
}

// A bare filename is a shorthand, not a repo-root path.
//
// Also not hypothetical: matching bare filenames reported 262 dangling
// citations against Catapult, every one of them correct —
// `dsl-syntax.md §15.1` means `docs/dsl-syntax.md`. It is the same class
// as `v5 §7.8` and waits on the same declared map.
func TestABareFilenameIsNotTreatedAsARepoRootPath(t *testing.T) {
	root := world(t)
	write(t, root, "test/thing_test.exs", "# dsl-syntax.md §15.1 and v5-design-decisions.md §9.9\n")

	got, err := Sweep(root, []string{"test/thing_test.exs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none — bare filenames are shorthand", got)
	}
}
