package decisions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Front matter is a `- `-item list of path globs. A walk that did not
// drop it would report `lib/dashboard/**` as a standing decision in
// every doc that maps anything — which is every doc.
func TestFrontMatterIsNotADecision(t *testing.T) {
	got := Index(`---
paths:
- lib/catapult/dashboard/**
- lib/catapult_web/live/**
---

# dashboard

## Standing decisions

- **Reads projections only**; every mutation goes through engine commands.
`)
	for _, e := range got {
		if strings.Contains(e, "lib/") {
			t.Errorf("a path glob was indexed as a decision: %q (all: %v)", e, got)
		}
	}
	if len(got) != 2 || got[0] != "Standing decisions" || got[1] != "Reads projections only" {
		t.Errorf("index = %v", got)
	}
}

// The h1 is the doc's name, which the caller already prints.
func TestTheDocTitleIsNotAnEntry(t *testing.T) {
	for _, e := range Index("# my-queue\n\n## Two tabs\n") {
		if e == "my-queue" {
			t.Error("the h1 was indexed")
		}
	}
}

func TestALeadIsTheBoldClaimOrTheFirstSentence(t *testing.T) {
	got := Index(`## Standing decisions

- **Cross-project, deliberately** (ORC-87). A node id is a per-project
  slug, so a query missing the project resolves nothing.
- Empty is a real state. It renders as such rather than as a spinner.
`)
	want := []string{"Standing decisions", "Cross-project, deliberately", "Empty is a real state."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("index = %v, want %v", got, want)
	}
}

// A decision written without a bold lead falls back to its first
// sentence, and these docs wrap. Without the continuation branch the
// lead stops at the line break, which turns a claim into half a claim —
// the sub-bullets and the blank line in the middle must not end it
// either.
func TestAWrappedDecisionIsIndexedWhole(t *testing.T) {
	got := Index(`## Standing decisions

- The action-needed set is enumerated, and nothing
  else is emitted:
  - sign off
  - unblock

  anything outside it is a row rather than an action. A second sentence.
- **Empty is a real state**
`)
	want := []string{
		"Standing decisions",
		"The action-needed set is enumerated, and nothing else is emitted: sign off unblock anything outside it is a row rather than an action.",
		"Empty is a real state",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("index  = %v\nwanted = %v", got, want)
	}
}

// A doc explaining the format may show a heading or a bullet inside a
// fence without having one — the same reading doclint gives fences.
func TestFencedExamplesAreNotDecisions(t *testing.T) {
	got := Index("## Real\n\n```md\n## Not real\n- **Not a decision**\n```\n")
	if len(got) != 1 || got[0] != "Real" {
		t.Errorf("index = %v", got)
	}
}

func TestAnEntryIsClipped(t *testing.T) {
	long := strings.Repeat("word ", 200)
	got := Index("## Standing decisions\n\n- **" + long + "**\n")
	if len(got[1]) > entryLimit+len("…") {
		t.Errorf("entry is %d bytes, limit is %d", len(got[1]), entryLimit)
	}
	if !strings.HasSuffix(got[1], "…") {
		t.Error("a clipped entry must say it was clipped")
	}
}

// A first design pass carries no mutex labels — the design pass is what
// creates them (DESIGN §6) — so a selection that only matched labels
// would show design nothing, and design is the pass this index is for.
func TestAFirstPassSelectsByTheTicketsOwnWords(t *testing.T) {
	docs := []Doc{
		{Path: "systems/dashboard.md", Name: "dashboard", Label: "system:dashboard"},
		{Path: "systems/delivery.md", Name: "delivery", Label: "system:delivery"},
	}
	shown, rest := Select(docs, nil, "Assignee projection for the dashboard")
	if len(shown) != 1 || shown[0].Name != "dashboard" {
		t.Errorf("shown = %v", shown)
	}
	if len(rest) != 1 || rest[0].Name != "delivery" {
		t.Errorf("rest = %v — an unselected doc is named, not dropped", rest)
	}
}

func TestLoadDirIndexesTheTree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "systems"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, "systems", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("dashboard.md", "## Standing decisions\n\n- **Reads projections only**\n")
	// README is prose about the directory, not a doc — filemap.LoadDir
	// skips it and so does this.
	write("README.md", "## Not a doc\n")
	docs, bad := LoadDir(root, "systems", "system:")
	if len(bad) != 0 {
		t.Errorf("unreadable = %v", bad)
	}
	if len(docs) != 1 || docs[0].Label != "system:dashboard" || docs[0].Path != "systems/dashboard.md" {
		t.Fatalf("docs = %+v", docs)
	}
	// A directory that does not exist indexes nothing rather than
	// failing: a project without screen docs has none to be shown.
	if docs, _ := LoadDir(root, "screens", "screen:"); len(docs) != 0 {
		t.Errorf("a missing directory indexed %v", docs)
	}
}
