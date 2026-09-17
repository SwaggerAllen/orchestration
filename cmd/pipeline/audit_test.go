package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
)

// project writes a minimal but real project tree — config, one system
// doc, one screen doc — and returns its root.
func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	raw, err := json.Marshal(config.Sample())
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pipeline.config.json", string(raw))
	write("systems/billing.md", "---\npaths:\n  - lib/app/billing/**\n---\n\n# billing\n\n## Standing decisions\n\n- Prose.\n")
	write("screens/home.md", "---\nfiles:\n  - lib/sample_web/components/home.ex\n---\n\n# home\n\n## Standing decisions\n\n- Prose.\n")
	return root
}

func listFile(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "list")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// CI runs the audit as `go -C .pipeline run ./cmd/pipeline audit`,
// which puts the process's working directory inside the *pipeline*
// checkout — a tree with no systems/ or screens/. With --root
// defaulting to ".", the audit loaded zero maps, and zero maps cannot
// be violated: it printed "audit clean ... against 0 system and 0
// screen maps" and exited 0 on a diff that should have failed. The
// check passed because it was looking somewhere the answer could not
// be, which is the worst way for a check to be wrong — it is
// indistinguishable from working.
func TestAuditFindsTheProjectsDocsFromAnyWorkingDirectory(t *testing.T) {
	root := project(t)
	t.Chdir(t.TempDir()) // stand anywhere else, as the real run does

	err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--changed-files", listFile(t, "lib/app/billing/invoice.ex"),
		"--labels", "screen:home", // the wrong label for a billing path
	})
	if err == nil {
		t.Fatal("audit passed a mapped path carrying the wrong label — it found no maps to check against")
	}
	if !strings.Contains(err.Error(), "violations") {
		t.Errorf("error = %v, want the violation report", err)
	}
}

// The same diff with the label it needs must pass, or the test above
// would be satisfied by an audit that simply always fails.
func TestAuditPassesWhenTheLabelCoversTheTouch(t *testing.T) {
	root := project(t)
	t.Chdir(t.TempDir())

	if err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--changed-files", listFile(t, "lib/app/billing/invoice.ex"),
		"--labels", "system:billing",
	}); err != nil {
		t.Errorf("audit failed a covered touch: %v", err)
	}
}

// The doc lint rides on the same root, so it fails the same way and
// must be covered the same way.
func TestAuditRunsTheDocLintAgainstTheProjectsDocs(t *testing.T) {
	root := project(t)
	if err := os.WriteFile(filepath.Join(root, "screens", "home.md"),
		[]byte("---\nfiles:\n  - x.ex\n---\n\n# home\n\n## States\n\n- empty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--changed-files", listFile(t, "README.md"),
		"--labels", "system:billing",
	})
	if err == nil {
		t.Fatal("audit passed a screen doc carrying a state section")
	}
}

// The citation sweep reaches outside docs/, which is the finding
// ORC-143 says matters most and the one the natural scoping misses.
//
// Catapult's own manual audit swept docs/, systems/, CLAUDE.md and
// bundles/, reported clean, and left 22 dangling citations in lib/,
// components/ and test/. Two sat inside error message strings, so the
// project's own audit was telling developers to read an entry that no
// longer existed.
func TestAuditResolvesSectionCitationsOutsideTheDocsTree(t *testing.T) {
	root := project(t)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "spec.md"),
		[]byte("# Spec\n## 7. Machinery\n### 7.8 Containers\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A dangling citation in lib/, not in docs/ — and inside a string
	// literal, which is where the two worst real ones were.
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "audit.ex"),
		[]byte(`raise "see docs/spec.md §9.9 for the rule"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--root", root,
		"--changed-files", listFile(t, "README.md"),
		"--labels", "system:billing",
	})
	if err == nil {
		t.Fatal("audit passed a citation of docs/spec.md §9.9, which that document does not have — " +
			"scoped to docs/ this is exactly the class that stays dangling")
	}
}

// An explicit --root still wins: the agents run the audit by hand from
// inside a project checkout, and that has to keep working.
func TestAuditHonoursAnExplicitRoot(t *testing.T) {
	root := project(t)
	t.Chdir(t.TempDir())

	if err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--root", root,
		"--changed-files", listFile(t, "lib/app/billing/invoice.ex"),
		"--labels", "system:billing",
	}); err != nil {
		t.Errorf("explicit --root failed: %v", err)
	}
}

// The design ownership audit's own list. The mutex audit reads the
// whole diff; this one reads only what the design agent committed,
// because dev may amend design-owned files on discovery (DESIGN §5) and
// the plain diff cannot say whose a path was.
func TestAuditFailsADesignCommitOutsideTheOwnedPaths(t *testing.T) {
	root := project(t)
	t.Chdir(t.TempDir())

	var err error
	out := captureStderr(t, func() {
		err = cmdAudit([]string{
			"--config", filepath.Join(root, "pipeline.config.json"),
			"--changed-files", listFile(t, "screens/home.md", "lib/sample/greetings.ex"),
			"--design-files", listFile(t, "screens/home.md", "lib/sample/greetings.ex"),
			"--labels", "screen:home",
		})
	})
	if err == nil {
		t.Fatal("audit passed a design commit carrying implementation")
	}
	// Asserted on the report rather than on the exit code: the same
	// paths could fail this audit for a mutex reason, and a test that
	// only checked "it failed" would keep passing after the ownership
	// check was removed.
	if !strings.Contains(out, "outside the paths design owns") {
		t.Errorf("the failure is not the ownership one: %s", out)
	}
	if strings.Contains(out, "screens/home.md") {
		t.Errorf("an owned path was reported as a stray: %s", out)
	}
}

// The same paths, written by dev rather than by design, are the
// amendment right working as intended — and must not fail. Without this
// the test above is satisfied by an audit that fails any diff reaching
// outside designOwnedPaths, which would fail every dev pass there is.
func TestAuditAllowsDevToTouchTheSamePaths(t *testing.T) {
	root := project(t)
	t.Chdir(t.TempDir())

	if err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--changed-files", listFile(t, "screens/home.md", "lib/sample/greetings.ex"),
		"--design-files", listFile(t), // design committed nothing in this PR
		"--labels", "screen:home",
	}); err != nil {
		t.Errorf("audit failed a dev pass amending a design-owned doc: %v", err)
	}
}

// A skipped check must not read as a passed one. The mutex audit spent
// its early life printing "clean ... against 0 system and 0 screen maps"
// from inside the wrong directory; the same shape of mistake here is an
// ownership rule nobody notices is off.
func TestAuditSaysWhenItSkippedTheDesignOwnershipCheck(t *testing.T) {
	root := project(t)
	t.Chdir(t.TempDir())

	out := captureStdout(t, func() {
		if err := cmdAudit([]string{
			"--config", filepath.Join(root, "pipeline.config.json"),
			"--changed-files", listFile(t, "screens/home.md"),
			"--labels", "screen:home",
		}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "design ownership audit: skipped") {
		t.Errorf("a run given no --design-files reported no skip: %s", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	var b strings.Builder
	if _, err := io.Copy(&b, r); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	var b strings.Builder
	if _, err := io.Copy(&b, r); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The wiring test: a shorthand in a project's own config has to reach
// the sweep, or steps 1 and 2 pass their unit tests separately while the
// gate checks nothing new. Both halves in one run — a live shorthand
// stays silent, a dangling one fails the audit.
func TestAuditResolvesShorthandCitationsFromTheProjectConfig(t *testing.T) {
	root := project(t)
	cfg := config.Sample()
	cfg.CitationShorthands = map[string]config.CitationShorthand{
		"v5":     {Path: "docs/spec.md"},
		"DESIGN": {Unchecked: "another repository's docs"},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pipeline.config.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "spec.md"),
		[]byte("# Spec\n## 7. Machinery\n### 7.8 Containers\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	// v5 §7.8 resolves; DESIGN §5 is unchecked and must stay silent.
	if err := os.WriteFile(filepath.Join(root, "lib", "ok.ex"),
		[]byte("# v5 §7.8 and DESIGN §5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	args := []string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--root", root,
		"--changed-files", listFile(t, "README.md"),
		"--labels", "system:billing",
	}
	if err := cmdAudit(args); err != nil {
		t.Fatalf("audit failed on a live shorthand and an unchecked one: %v", err)
	}

	// Now dangle it.
	if err := os.WriteFile(filepath.Join(root, "lib", "ok.ex"),
		[]byte("# v5 §9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdAudit(args); err == nil {
		t.Fatal("audit passed v5 §9.9 — the config's shorthand map never reached the sweep")
	}
}

// A shorthand nothing cites is reported, and the audit still passes.
//
// Both halves are the test. The report is the point of the check; the
// exit code is the constraint that makes it shippable, because the only
// file that can answer it — `pipeline.config.json` — is author-owned
// (DESIGN §5). Gating would leave a ticket red with every file it
// needed closed to it.
func TestAuditProposesAnUncitedShorthandWithoutFailing(t *testing.T) {
	root := project(t)
	cfg := config.Sample()
	cfg.CitationShorthands = map[string]config.CitationShorthand{
		"v5":     {Path: "docs/spec.md"},
		"v4":     {Path: "docs/spec.md"},
		"DESIGN": {Unchecked: "another repository's docs"},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pipeline.config.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "spec.md"),
		[]byte("# Spec\n## 7. Machinery\n### 7.8 Containers\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	// v5 is cited and DESIGN is cited-but-unchecked, so both are in
	// use. v4 is named nowhere before a §, and `v4` in this sentence is
	// not a use either.
	if err := os.WriteFile(filepath.Join(root, "lib", "ok.ex"),
		[]byte("# v5 §7.8, DESIGN §5, and a bare mention of v4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	var auditErr error
	out := captureStdout(t, func() {
		auditErr = cmdAudit([]string{
			"--config", filepath.Join(root, "pipeline.config.json"),
			"--root", root,
			"--changed-files", listFile(t, "README.md"),
			"--labels", "system:billing",
		})
	})
	if auditErr != nil {
		t.Fatalf("an uncited shorthand failed the audit: %v — it is a proposal, and its fix is author-owned", auditErr)
	}
	// Read the prune line alone. The audit's other lines cite DESIGN §5
	// themselves, so a substring search over the whole output would
	// pass whatever the check reported.
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "citation shorthands") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no prune line in the audit's output: %s", out)
	}
	names, _, _ := strings.Cut(strings.TrimPrefix(line, "citation shorthands no citation names: "), " —")
	if names != "v4" {
		t.Errorf("prune candidates = %q, want just v4: v5 and DESIGN are both cited", names)
	}
}

// The sweep must not read a checkout of another repository, and must
// still read the project.
//
// Both halves are the test, and the second is the one that would rot.
// `setup-pipeline` checks the pipeline out at `.pipeline` inside the
// project workspace, so the sweep read the citation checker's own test
// fixtures — citations that dangle *on purpose*, since that is what
// they are for. No version of that repository passes this check, so
// every ticket branch in every project failed on violations that were
// none of the project's. A fix that skipped too much would pass the
// first half of this test in exactly the way the audit has failed
// before: by reporting clean from somewhere the answer could not be.
func TestTheSweepSkipsANestedCheckoutButStillReadsTheProject(t *testing.T) {
	root := project(t)
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A checkout, marked the way actions/checkout marks one.
	write(".pipeline/.git/HEAD", "ref: refs/heads/main\n")
	write(".pipeline/internal/citations/citations_test.go", "# docs/gone.md §1.1\n")
	// One nested deeper than the top level, so the rule is not "the
	// directory called .pipeline".
	write("vendor/other/.git/HEAD", "ref: refs/heads/main\n")
	write("vendor/other/lib/thing.ex", "# docs/gone.md §2.2\n")
	// And the project's own, which must still be reported.
	write("lib/mine.ex", "# docs/gone.md §3.3\n")
	t.Chdir(t.TempDir())

	args := []string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--root", root,
		"--changed-files", listFile(t, "README.md"),
		"--labels", "system:billing",
	}
	var auditErr error
	out := captureStderr(t, func() { auditErr = cmdAudit(args) })
	if auditErr == nil {
		t.Fatal("the project's own dangling citation did not fail the audit — the sweep skipped too much")
	}
	if strings.Contains(out, ".pipeline") || strings.Contains(out, "vendor/other") {
		t.Errorf("a nested checkout's citations were audited as the project's:\n%s", out)
	}
	if !strings.Contains(out, "lib/mine.ex") {
		t.Errorf("the project's own dangling citation is not reported:\n%s", out)
	}
}

// portedProject is project(t) with its system doc carrying ids and a
// reasons sibling, so the rationale index's checks are armed on it.
func portedProject(t *testing.T) string {
	t.Helper()
	root := project(t)
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("systems/billing.md", "---\npaths:\n  - lib/app/billing/**\n---\n\n# billing\n\n## #1 Standing decisions\n\n- **#17 The cap is enforced server-side.** Prose.\n\n## #2 Depends on\n\n- nothing.\n")
	write("systems/billing.reasons.md", "# billing — reasons\n\n## #17\nsince: ORC-22\n\nA second copy of the rules drifts.\n\n## #9\nretired: ORC-90 — the guard moved into the compiler\n")
	return root
}

func runAudit(t *testing.T, root string, extra ...string) (string, error) {
	t.Helper()
	t.Chdir(t.TempDir())
	var err error
	out := captureStdout(t, func() {
		err = cmdAudit(append([]string{
			"--config", filepath.Join(root, "pipeline.config.json"),
			"--root", root,
			"--changed-files", listFile(t, "README.md"),
			"--labels", "system:billing",
		}, extra...))
	})
	return out, err
}

// The index's checks skip an unported tree and say so. "Nothing is
// ported" and "everything checked out" must not print the same, because
// the port lands one doc at a time and a green audit on an unported doc
// is not evidence about it.
func TestAuditSaysTheReasonsIndexIsSkippedOnAnUnportedTree(t *testing.T) {
	out, err := runAudit(t, project(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "reasons index: skipped") {
		t.Errorf("no skip line: %s", out)
	}
}

func TestAuditNamesTheUnportedDocsItSkipped(t *testing.T) {
	out, err := runAudit(t, portedProject(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "reasons index: 1 doc(s) checked; skipped 1 unported: screens/home.md") {
		t.Errorf("out = %s", out)
	}
}

func TestAuditHoldsAPortedDocToItsIds(t *testing.T) {
	cases := []struct{ name, doc, reasons, want string }{
		{"a heading without an id", "# billing\n\n## #1 Standing decisions\n\n- **#17 Lead.**\n\n## Depends on\n", "", `systems/billing.md:12: heading "Depends on" carries no id`},
		{"a standing decision without an id", "# billing\n\n## #1 Standing decisions\n\n- **Lead without a number.** Prose.\n", "", `systems/billing.md:10: standing decision "Lead without a number." carries no id`},
		{"a duplicate id", "# billing\n\n## #1 Standing decisions\n\n- **#1 Lead.**\n", "", `systems/billing.md: id #1 appears 2 times (lines 8, 10)`},
		{"an entry whose rule is gone and not retired", "# billing\n\n## #1 Standing decisions\n", "## #17\n\nOrphan.\n", `entry #17 has no rule line in systems/billing.md and is not retired`},
		{"a retired entry whose rule line survives", "# billing\n\n## #1 Standing decisions\n\n- **#9 Still here.**\n", "## #9\nretired: ORC-90 — gone\n", `entry #9 is retired (ORC-90 — gone) but systems/billing.md still carries rule #9`},
		{"a reasons heading that is not an id", "# billing\n\n## #1 Standing decisions\n", "## Why\n", `systems/billing.reasons.md:1: heading "Why" is not an entry`},
		{"a duplicate entry", "# billing\n\n## #1 Standing decisions\n\n- **#17 Lead.**\n", "## #17\n\nA.\n\n## #17\n\nB.\n", `systems/billing.reasons.md: entry #17 appears 2 times (lines 1, 5)`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := project(t)
			if err := os.WriteFile(filepath.Join(root, "systems", "billing.md"), []byte("---\npaths:\n  - lib/app/billing/**\n---\n\n"+c.doc), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.reasons != "" {
				if err := os.WriteFile(filepath.Join(root, "systems", "billing.reasons.md"), []byte(c.reasons), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			stderr := captureStderr(t, func() { _, err = runAudit(t, root) })
			if err == nil {
				t.Fatalf("the audit passed; stderr: %s", stderr)
			}
			if !strings.Contains(stderr, c.want) {
				t.Errorf("stderr = %s\nwant %q", stderr, c.want)
			}
		})
	}
}

// Both directions: the well-formed pair passes, including a retired
// entry with no rule line, which is what retirement looks like.
func TestAuditPassesAPortedDocWhoseIdsAllHold(t *testing.T) {
	out, err := runAudit(t, portedProject(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "audit clean") {
		t.Errorf("out = %s", out)
	}
}

// The whole-tree sweep resolves rule citations wherever they are written
// — a `.ex` file here — and the whitelist keeps Catapult's PR numbers
// and colour codes out of it.
func TestAuditResolvesARuleCitationAnywhereInTheTree(t *testing.T) {
	root := portedProject(t)
	write := func(rel, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lib/ok.ex", "# billing#17 is a rule; billing#9 is a retired entry; PR #144, pre-#144, `#17ff00`, home#1 is unported\n")
	if out, err := runAudit(t, root); err != nil {
		t.Fatalf("live citations failed the audit: %v\n%s", err, out)
	}
	write("lib/bad.ex", "# billing#99 names nothing\n")
	var err error
	stderr := captureStderr(t, func() { _, err = runAudit(t, root) })
	if err == nil || !strings.Contains(stderr, "lib/bad.ex:1: cites billing#99, which is not a rule in systems/billing.md or an entry in systems/billing.reasons.md") {
		t.Errorf("err = %v, stderr = %s", err, stderr)
	}
}

func TestAuditReportsAnAmbiguousUnprefixedRuleCitationAndResolvesAPrefixedOne(t *testing.T) {
	root := portedProject(t)
	write := func(rel, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("screens/billing.md", "---\nfiles:\n  - lib/sample_web/components/billing.ex\n---\n\n# billing\n\n## #1 One screen\n")
	write("lib/ok.ex", "# system:billing#17 and screen:billing#1\n")
	if out, err := runAudit(t, root); err != nil {
		t.Fatalf("prefixed citations failed: %v\n%s", err, out)
	}
	write("lib/bad.ex", "# billing#17\n")
	var err error
	stderr := captureStderr(t, func() { _, err = runAudit(t, root) })
	if err == nil || !strings.Contains(stderr, "cites billing#17, which is ambiguous: billing is both systems/billing.md and screens/billing.md — write system:billing or screen:billing") {
		t.Errorf("err = %v, stderr = %s", err, stderr)
	}
}

// A reasons file is swept too: an entry may cite another rule, and a
// dangling one there rots as fast as anywhere.
func TestAuditSweepsReasonsFilesForRuleCitations(t *testing.T) {
	root := portedProject(t)
	if err := os.WriteFile(filepath.Join(root, "systems", "billing.reasons.md"), []byte("## #17\n\nSee billing#77.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var err error
	stderr := captureStderr(t, func() { _, err = runAudit(t, root) })
	if err == nil || !strings.Contains(stderr, "systems/billing.reasons.md:3: cites billing#77") {
		t.Errorf("err = %v, stderr = %s", err, stderr)
	}
}

// The port's progress is a number, printed per ported doc and never
// gated: the budget is the port's acceptance criterion, not a rule a
// ticket can be failed on (DESIGN §4).
func TestAuditReportsRuleSideBytesPerPortedDoc(t *testing.T) {
	out, err := runAudit(t, portedProject(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "reasons index sizes: systems/billing.md 0.2 KB rules / 0.1 KB reasons") {
		t.Errorf("out = %s", out)
	}
	if strings.Contains(out, "screens/home.md 0") {
		t.Error("an unported doc was sized; the report is about the port's progress")
	}
}

// The manual test set's reachability rides on the same root, so it fails
// the same way and is covered the same way.
//
// This is the wiring, not the rule: internal/manualtest tests Problems
// itself. Reverting the one line that appends its findings to the audit's
// violations printed `ok` against a suite that covered the rule
// thoroughly — the shape CLAUDE.md records for awaitingDispatchOf and for
// the host-to-core mapping, where each half was asserted and the crossing
// was not.
func TestAuditGatesManualTestReachability(t *testing.T) {
	root := project(t)
	mustWrite := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func() error {
		return cmdAudit([]string{
			"--config", filepath.Join(root, "pipeline.config.json"),
			"--changed-files", listFile(t, "lib/app/billing/invoice.ex"),
			"--labels", "system:billing",
		})
	}

	// Reachable: `system:billing` is what systems/billing.md declares.
	mustWrite("tests/manual/invoice-renders.md", "---\ncovers:\n  - system:billing\n---\n\n# renders\n")
	if err := run(); err != nil {
		t.Fatalf("a reachable manual test failed the audit: %v", err)
	}

	// Unreachable: no doc declares this seam, so no diff selects it and
	// it can never fail — an unchecked claim, which is the pile §8.2's
	// argument depends on not existing.
	mustWrite("tests/manual/orphan.md", "---\ncovers:\n  - system:billling\n---\n\n# typo\n")
	var err error
	// Violations go to stderr and the error carries only the count —
	// this command's existing shape, so the detail is asserted where the
	// audit actually puts it. A reader fixing this needs the file and the
	// seam, and "1 violations" is neither.
	out := captureStderr(t, func() { err = run() })
	if err == nil {
		t.Fatal("a manual test covering a seam no doc declares must fail the audit")
	}
	for _, want := range []string{"tests/manual/orphan.md", "billling", "no system or screen doc declares"} {
		if !strings.Contains(out, want) {
			t.Errorf("the audit's output does not name %q:\n%s", want, out)
		}
	}
	// Repo-relative, so the reader is told which file to fix rather than
	// where the runner put its checkout.
	if strings.Contains(out, root) {
		t.Errorf("the violation names the runner's absolute path:\n%s", out)
	}
}

// "This project has no manual tests yet" and "every manual test is
// reachable" must not print the same line — the distinction the class
// audit's own skips exist for.
func TestAuditSaysItSkippedManualTestsRatherThanPassingThem(t *testing.T) {
	root := project(t)
	out := captureStdout(t, func() {
		if err := cmdAudit([]string{
			"--config", filepath.Join(root, "pipeline.config.json"),
			"--changed-files", listFile(t, "lib/app/billing/invoice.ex"),
			"--labels", "system:billing",
		}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "manual tests: skipped") {
		t.Errorf("no skip line for a project with no manual tests:\n%s", out)
	}
}
