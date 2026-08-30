package filemap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	v := Audit(systems, screens,
		[]string{"lib/app/billing/cap.ex", "storybook/cap.story.exs", "lib/unowned/router.ex"},
		[]string{"system:billing", "screen:cap"})
	if len(v) != 0 {
		t.Errorf("clean diff flagged: %v", v)
	}

	// Missing labels are named specifically.
	v = Audit(systems, screens,
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
	v = Audit(overlap, nil, []string{"lib/app/billing/cap.ex"}, []string{"system:billing", "system:billing2"})
	found := false
	for _, s := range v {
		if strings.Contains(s, "two system docs") {
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
	corpus := [][]string{
		nil,
		{"README.md"},
		{"lib/engine/retry.ex"},
		{"lib/delivery/queue.ex", "config/delivery.exs"},
		{"lib/web/home/index.ex", "storybook/screens/cap/component.ex", "lib/dsl/parse.ex"},
		{"lib/engine/a.ex", "lib/engine/b.ex", "mix.exs", "lib/web/home/x.ex"},
	}
	for _, changed := range corpus {
		want := OwnerLabels(systems, screens, changed)
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
			for _, v := range Audit(systems, screens, changed, others) {
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
		for _, v := range Audit(systems, screens, changed, want) {
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
