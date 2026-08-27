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

	got, err := Sweep(root, []string{"lib/thing.ex"}, nil)
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

	got, err := Sweep(root, []string{"lib/audit.ex", "bundles/default/tiers/impl.yaml", "test/support/case.ex"}, nil)
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

	got, err := Sweep(root, []string{"lib/thing.ex"}, nil)
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

	got, err := Sweep(root, []string{"lib/thing.ex"}, nil)
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

	got, err := Sweep(root, []string{"lib/thing.ex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none — §9.9 belongs to another sentence", got)
	}
}

// With no map declared, a shorthand resolves to nothing and reports
// nothing. That is the state every project sits in until it writes the
// table, and it must stay silent rather than becoming 896 failures the
// day the resolver ships.
func TestAShorthandIsSkippedWhenTheProjectDeclaresNoMap(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# v5 §9.9 and conventions §2 are not checked here\n")

	got, err := Sweep(root, []string{"lib/thing.ex"}, nil)
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

	got, err := Sweep(root, []string{"systems/thing.md"}, nil)
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
// as `v5 §7.8`, and it resolves through the declared map or not at all.
func TestABareFilenameIsNotTreatedAsARepoRootPath(t *testing.T) {
	root := world(t)
	write(t, root, "test/thing_test.exs", "# dsl-syntax.md §15.1 and v5-design-decisions.md §9.9\n")

	got, err := Sweep(root, []string{"test/thing_test.exs"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none — bare filenames are shorthand", got)
	}
}

// catapult is the map Catapult ships, reduced to what Sweep is handed:
// path-bearing entries only. The `unchecked` ones — DESIGN, AGPL,
// orchestration — are dropped by the caller and so are simply absent
// here, which is the point.
func catapult() map[string]string {
	return map[string]string{
		"v5":            "docs/v5-design-decisions.md",
		"conventions":   "docs/conventions.md",
		"Conventions":   "docs/conventions.md",
		"dsl-syntax.md": "docs/dsl-syntax.md",
	}
}

// The payoff. Shorthands outnumber explicit paths roughly 27 to 1 in
// Catapult's tree (896 against 33), so until this resolves the check
// covers a rounding error of the corpus.
func TestAShorthandResolvesThroughTheDeclaredMap(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# v5 §7.8 is live; v5 §9.9 is not\n")

	got, err := Sweep(root, []string{"lib/thing.ex"}, catapult())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Sweep = %v, want only the dangling §9.9", got)
	}
	// Named as written and as resolved: the writer searches for what
	// they typed, the reader fixing it needs the file.
	msg := got[0].String()
	if !strings.Contains(msg, "v5 (docs/v5-design-decisions.md)") {
		t.Errorf("report names neither the shorthand nor the file: %s", msg)
	}
}

// A filename-shaped key. `dsl-syntax.md` is the second-largest class in
// Catapult (190), and there is no such file at the repo root — a map
// typed {word: path} would exclude exactly the case most in need of it.
func TestAFilenameShapedKeyResolves(t *testing.T) {
	root := world(t)
	write(t, root, "docs/dsl-syntax.md", "## 15. Grammar\n### 15.10 Sub-arrays\n")
	write(t, root, "lib/thing.ex", "# dsl-syntax.md §15.10 and dsl-syntax.md §15.99\n")

	got, err := Sweep(root, []string{"lib/thing.ex"}, catapult())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Section != "15.99" {
		t.Fatalf("Sweep = %v, want only §15.99", got)
	}
}

// Lookup is case-sensitive, and this is the case that forces it.
// `design` is an ordinary word in these corpora; folding case would let
// prose resolve against a DESIGN entry and start reporting sentences.
// It occurs zero times before a § in Catapult today, so the collision is
// latent — which is why it is asserted rather than left to be noticed.
func TestLookupIsCaseSensitive(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# the design §4 said so, and Conventions §2 is a real citation\n")

	m := catapult()
	m["DESIGN"] = "docs/v5-design-decisions.md" // as if DESIGN named a real file
	write(t, root, "docs/conventions.md", "## 2. Rules\n")

	got, err := Sweep(root, []string{"lib/thing.ex"}, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v — lowercase `design` must not resolve against the DESIGN key", got)
	}
}

// An unchecked entry never reaches Sweep, so it behaves exactly as a
// token the map has never heard of. Asserted because the alternative —
// a third branch inside the resolver — is what an implementation reaches
// for when the ticket says "three states", and there are only two.
func TestAnUncheckedShorthandBehavesExactlyLikeAnAbsentOne(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# DESIGN §5 and AGPL §7 and neverheardof §3\n")

	got, err := Sweep(root, []string{"lib/thing.ex"}, catapult())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none — none of these is a path-bearing key", got)
	}
}

// The whitelist is what keeps prose out. 285 bare back-references across
// 120 ordinary English words sit before a § in Catapult's tree; none is
// a citation, and a resolver that treated the preceding word as a
// document name would report the corpus.
func TestBareBackReferencesAreNotLookups(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# see §2, and §3, per §7, as §9.9 of this file says\n")

	got, err := Sweep(root, []string{"lib/thing.ex"}, catapult())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none — see/and/per/as are prose, not documents", got)
	}
}

// A declared shorthand pointing at a file that is not there is a
// misconfigured map, and reporting it is the whole reason the entry is
// declared rather than inferred.
func TestAShorthandNamingAMissingDocumentIsReported(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# conventions §2\n")

	got, err := Sweep(root, []string{"lib/thing.ex"}, catapult())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Why, "does not have") {
		t.Fatalf("Sweep = %v, want the missing docs/conventions.md reported", got)
	}
}

// A section number may carry a part letter. Catapult's vendored v4 spec
// numbers every heading `A.1.4` / `B.3.2`, and 17 citations of it write
// the letter. Measured: before this, all 17 resolved against nothing and
// were reported as dangling — the check manufacturing exactly the false
// failure it exists to prevent.
func TestAPartLetteredSectionResolves(t *testing.T) {
	root := world(t)
	write(t, root, "seed-docs/v4.md", "# Spec\n# Part A\n## A.3 Bundles\n### A.3.3 Tier declarations\n")
	write(t, root, "lib/thing.ex", "# see seed-docs/v4.md §A.3.3 and seed-docs/v4.md §A.3\n")

	got, err := Sweep(root, []string{"lib/thing.ex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Sweep = %v, want none — A.3.3 is a heading and A.3 is its parent", got)
	}
}

// Citing deeper than the document goes is still dangling. Prefixes
// resolve upward — §7 is satisfied by §7.8 — but §7.12.1 is not
// satisfied by §7.12. Measured on Catapult: 19 citations of a v5
// subsection that does not exist, all of one number.
func TestCitingASubsectionThatDoesNotExistIsReported(t *testing.T) {
	root := world(t)
	write(t, root, "lib/thing.ex", "# docs/v5-design-decisions.md §7.8.1\n")

	got, err := Sweep(root, []string{"lib/thing.ex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Sweep = %v, want §7.8.1 reported — §7.8 existing does not satisfy it", got)
	}
}
