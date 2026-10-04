package filemap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// records builds a Records over the real kind table, so a test corpus
// carries the same Exclusive flags the audit runs against.
func records(systems, screens, dsl []Doc) Records {
	var r Records
	for _, k := range protocol.RecordKinds {
		var docs []Doc
		switch k.Dir {
		case "systems":
			docs = systems
		case "screens":
			docs = screens
		case "docs/dsl":
			docs = dsl
		}
		r.Kinds = append(r.Kinds, KindDocs{Kind: k, Docs: docs})
	}
	return r
}

func TestParseFrontMatter(t *testing.T) {
	globs, err := ParseFrontMatter(`---
paths:
  - lib/app/billing/**
  - test/app/billing/**
---
# Billing
prose`)
	if err != nil {
		t.Fatal(err)
	}
	if len(globs) != 2 || globs[0] != "lib/app/billing/**" {
		t.Errorf("globs = %v", globs)
	}

	if globs, err := ParseFrontMatter("# no front matter\n"); err != nil || globs != nil {
		t.Errorf("docless = %v, %v", globs, err)
	}
	if _, err := ParseFrontMatter("---\npaths:\n  - x\n"); err == nil {
		t.Error("unterminated front matter must error")
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"lib/app/billing/**", "lib/app/billing/cap.ex", true},
		{"lib/app/billing/**", "lib/app/billing/deep/nest.ex", true},
		{"lib/app/billing/**", "lib/app/billing_x/cap.ex", false},
		{"screens/*.md", "screens/cap.md", true},
		{"screens/*.md", "screens/sub/cap.md", false},
		{"lib/app_web/components/cap.ex", "lib/app_web/components/cap.ex", true},
	}
	for _, c := range cases {
		if got := Match(c.glob, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestLoadDirSkipsReadme(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("billing.md", "---\npaths:\n  - lib/app/billing/**\n---\n")
	write("README.md", "prose about the dir")
	docs, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Name != "billing" {
		t.Errorf("docs = %+v", docs)
	}

	if docs, err := LoadDir(filepath.Join(dir, "missing")); err != nil || docs != nil {
		t.Errorf("missing dir = %v, %v", docs, err)
	}
}

func TestAudit(t *testing.T) {
	systems := []Doc{
		{Name: "billing", Globs: []string{"lib/app/billing/**"}},
		{Name: "search", Globs: []string{"lib/app/search/**"}},
	}
	screens := []Doc{
		{Name: "cap", Globs: []string{"lib/app_web/components/cap.ex", "storybook/cap.story.exs"}},
	}

	// Clean: labels cover the touches.
	v := Audit(records(systems, screens, nil),
		[]string{"lib/app/billing/cap.ex", "storybook/cap.story.exs", "lib/unowned/router.ex"},
		[]string{"system:billing", "screen:cap"})
	if len(v) != 0 {
		t.Errorf("clean diff flagged: %v", v)
	}

	// Missing labels are named specifically.
	v = Audit(records(systems, screens, nil),
		[]string{"lib/app/search/index.ex", "lib/app_web/components/cap.ex"},
		[]string{"system:billing"})
	if len(v) != 2 {
		t.Fatalf("violations = %v", v)
	}
	if !strings.Contains(v[0], "system:search") || !strings.Contains(v[1], "screen:cap") {
		t.Errorf("violations must name the missing label: %v", v)
	}

	// Overlapping ownership on a live path.
	overlap := append(systems, Doc{Name: "billing2", Globs: []string{"lib/app/billing/**"}})
	v = Audit(records(overlap, nil, nil), []string{"lib/app/billing/cap.ex"}, []string{"system:billing", "system:billing2"})
	found := false
	for _, s := range v {
		if strings.Contains(s, "two docs under systems/") {
			found = true
		}
	}
	if !found {
		t.Errorf("overlap not flagged: %v", v)
	}
}

// OwnerLabels reads Audit's "a changed path mapped by a doc requires
// that doc's label" rule forwards, and two implementations of one rule
// is how they drift. So: for any corpus, the labels OwnerLabels returns
// are exactly the ones whose absence Audit reports.
//
// Asserted in both directions on purpose. Narrowing OwnerLabels would
// release a mutex label the audit is about to demand back — the failure
// it exists to prevent — and widening it would hold one nothing needs,
// which is the failure the release exists to prevent.
func TestOwnerLabelsAgreesWithAudit(t *testing.T) {
	systems := []Doc{
		{Name: "delivery", Globs: []string{"lib/delivery/**", "config/delivery.exs"}},
		{Name: "engine", Globs: []string{"lib/engine/**"}},
		{Name: "core_dsl", Globs: []string{"lib/dsl/**"}},
	}
	screens := []Doc{
		{Name: "home", Globs: []string{"lib/web/home/**"}},
		{Name: "cap", Globs: []string{"storybook/screens/cap/*"}},
	}
	dsl := []Doc{
		{Name: "bundle", Globs: []string{"bundles/**"}},
		{Name: "chain", Globs: []string{"bundles/*/chain.yaml"}},
	}
	corpus := [][]string{
		nil,
		{"README.md"},
		{"lib/engine/retry.ex"},
		{"lib/delivery/queue.ex", "config/delivery.exs"},
		{"lib/web/home/index.ex", "storybook/screens/cap/component.ex", "lib/dsl/parse.ex"},
		{"lib/engine/a.ex", "lib/engine/b.ex", "mix.exs", "lib/web/home/x.ex"},
		{"bundles/default/chain.yaml"},
	}
	for _, changed := range corpus {
		want := OwnerLabels(records(systems, screens, dsl), changed)
		// What Audit demands, read off an empty label set: every mapped
		// path with no label is one violation naming that label.
		var got []string
		for _, l := range want {
			// Hand Audit every label but this one; if it is genuinely
			// required, exactly that one must come back missing.
			var others []string
			for _, o := range want {
				if o != l {
					others = append(others, o)
				}
			}
			missing := 0
			for _, v := range Audit(records(systems, screens, dsl), changed, others) {
				if strings.Contains(v, l) {
					missing++
				}
			}
			if missing == 0 {
				t.Errorf("OwnerLabels(%v) claims %q is required and Audit does not demand it", changed, l)
				continue
			}
			got = append(got, l)
		}
		// And the other direction: given every label OwnerLabels named,
		// Audit must have nothing left to say. Overlap is its only other
		// finding and no label answers it, so anything else is a label
		// the audit demands and the release would have taken off.
		for _, v := range Audit(records(systems, screens, dsl), changed, want) {
			if strings.Contains(v, "overlapping ownership") {
				continue
			}
			t.Errorf("Audit(%v) still demands a label OwnerLabels did not name: %s", changed, v)
		}
		if len(got) != len(want) {
			t.Errorf("OwnerLabels(%v) = %v, agreed on %v", changed, want, got)
		}
	}
}

// A reasons sibling (DESIGN §4) read as a doc is a phantom: measured by
// removing the skip, `systems/foundation.reasons.md` loads as
// Doc{Name: "foundation.reasons"} with no map — invisible to the audit,
// and a name the label resolver would accept as a real system.
func TestLoadDirSkipsReasonsFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("foundation.md", "---\npaths:\n  - lib/foundation/**\n---\n")
	write("foundation.reasons.md", "# foundation — reasons\n\n## #17\n\nBecause.\n")
	docs, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Name != "foundation" {
		t.Errorf("docs = %+v, want foundation alone", docs)
	}
}

// A non-exclusive kind's docs may map one path twice. The grammar
// contract is the case: bundle.md maps bundles/** and chain.md maps
// bundles/*/chain.yaml, so every chain file has two owners on purpose.
// Under the rule as it was written — "no path may be mapped by two
// system docs", applied to whatever list was passed first — adopting a
// third directory would have reported that overlap on every PR touching
// a bundle.
func TestANonExclusiveKindMayOverlap(t *testing.T) {
	dsl := []Doc{
		{Name: "bundle", Globs: []string{"bundles/**"}},
		{Name: "chain", Globs: []string{"bundles/*/chain.yaml"}},
	}
	v := Audit(records(nil, nil, dsl),
		[]string{"bundles/default/chain.yaml"},
		[]string{"dsl:bundle", "dsl:chain"})
	for _, s := range v {
		if strings.Contains(s, "overlapping ownership") {
			t.Errorf("overlap flagged on a non-exclusive kind: %v", v)
		}
	}
	// The labels are still each required.
	v = Audit(records(nil, nil, dsl), []string{"bundles/default/chain.yaml"}, []string{"dsl:bundle"})
	if len(v) != 1 || !strings.Contains(v[0], "dsl:chain") {
		t.Errorf("violations = %v, want the missing dsl:chain label", v)
	}
	if !strings.Contains(v[0], "docs/dsl/chain.md") {
		t.Errorf("violation must name the doc by its real path: %s", v[0])
	}
}
