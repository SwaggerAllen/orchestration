package filemap

import (
	"strings"
	"testing"
)

var catapultOwned = []string{"screens/**", "storybook/**", "systems/*.md", "docs/*.md"}

func TestDesignAuditFlagsWorkOutsideTheOwnedSet(t *testing.T) {
	// ORC-84's diff, in miniature: real design artifacts alongside the
	// implementation the pass kept going into.
	written := []string{
		"docs/dsl-syntax.md",
		"systems/core_dsl.md",
		"bundles/default/tiers/resp.yaml",
		"lib/catapult/dsl/edge.ex",
		"test/catapult/dsl/loader_test.exs",
	}
	got := DesignAudit(catapultOwned, written)
	if len(got) != 1 {
		t.Fatalf("violations = %d, want one finding naming them all: %v", len(got), got)
	}
	for _, want := range []string{"bundles/default/tiers/resp.yaml", "lib/catapult/dsl/edge.ex", "test/catapult/dsl/loader_test.exs"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the finding does not name %s:\n%s", want, got[0])
		}
	}
	for _, ok := range []string{"docs/dsl-syntax.md", "systems/core_dsl.md"} {
		if strings.Contains(got[0], ok) {
			t.Errorf("%s is design's own and was flagged:\n%s", ok, got[0])
		}
	}
	// The way out is a proposal, because the config is author-only and a
	// push touching it is rejected — a blocked pass that reaches for it
	// takes the run down.
	if !strings.Contains(got[0], "proposal") || !strings.Contains(got[0], "author-only") {
		t.Errorf("the finding does not say how to widen the set:\n%s", got[0])
	}
}

// Folders, not files. A design pass legitimately produces files nobody
// enumerated in advance, and a rule that listed extensions would fail on
// the first one — which is exactly what design.md's hardcoded
// `.heex`/`.story.exs` list did to any project whose design output is
// neither.
func TestDesignAuditAcceptsNewFilesInsideOwnedFolders(t *testing.T) {
	written := []string{
		"screens/checkout/cap_reached.heex",
		"screens/checkout/notes.md",
		"storybook/checkout.story.exs",
		"docs/dsl-syntax.md",
	}
	if got := DesignAudit(catapultOwned, written); got != nil {
		t.Errorf("a pass inside its own folders was flagged: %v", got)
	}
}

// systems/*.md is single-level by this matcher, and docs/*.md is chosen
// deliberately over docs/** so boundary's retro notes stay boundary's.
// The matcher has no exclusion syntax, so single-level is the only way
// to draw that line — worth pinning, since a later widening to ** would
// silently claim another role's output.
func TestDesignAuditLeavesTheRetroNotesToBoundary(t *testing.T) {
	got := DesignAudit(catapultOwned, []string{"docs/retros/hookup.md"})
	if got == nil {
		t.Fatal("docs/retros is boundary's output; docs/*.md must not cover it")
	}
	if got := DesignAudit(catapultOwned, []string{"systems/nested/thing.md"}); got == nil {
		t.Error("systems/*.md is single-level; a nested doc falls outside the set and should say so")
	}
}

// "No paths declared" and "stayed inside them" must not print the same.
func TestDesignAuditReportsAnUndeclaredBoundary(t *testing.T) {
	got := DesignAudit(nil, []string{"anything.ex"})
	if len(got) != 1 || !strings.Contains(got[0], "designOwnedPaths") {
		t.Errorf("an undeclared boundary passed silently: %v", got)
	}
	if got := DesignAudit(nil, nil); got != nil {
		t.Errorf("a pass that wrote nothing was flagged: %v", got)
	}
}
