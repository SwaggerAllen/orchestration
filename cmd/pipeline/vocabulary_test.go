package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/promptdoc"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func promptFiles(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "prompts", "*.md"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no prompts found: %v", err)
	}
	out := map[string]string{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = string(raw)
	}
	return out
}

// Every include resolves, in CI, at the moment somebody writes it.
//
// The runtime check fails closed and names the anchor, which keeps a
// broken include from costing a pass silently — but it fails after
// dispatch, on a run that has already been paid for. DESIGN and the
// prompts change in the same commit, so CI can see both halves and is
// the cheap place to catch this.
func TestEveryPromptIncludeResolvesAgainstDesign(t *testing.T) {
	blocks, err := promptdoc.Blocks(repoFile(t, "DESIGN.md"))
	if err != nil {
		t.Fatalf("DESIGN.md: %v", err)
	}
	used := map[string]bool{}
	for name, body := range promptFiles(t) {
		for _, id := range promptdoc.Includes(body) {
			used[id] = true
			if _, ok := blocks[id]; !ok {
				t.Errorf("prompts/%s includes %q and DESIGN.md does not define it", name, id)
			}
		}
	}
	// And the other direction: a list DESIGN grows that no prompt reads
	// is a list that can be wrong for free, which is the state the
	// bounded inputs were already in when this was written.
	for id, b := range blocks {
		if b.Reference {
			continue // `for=tests`: stated once, asserted, never inlined
		}
		if !used[id] {
			t.Errorf("DESIGN.md defines list %q and no prompt includes it", id)
		}
	}
}

// The value column of §13's outcome table, held to the code in both
// directions.
//
// Both directions matter and only one of them was ever checked. The
// proposal-kind guard asserted "everything the prompt offers, the parser
// accepts" and then restated the converse as a hardcoded slice — which
// was not updated when `bug` was added, so dropping `bug` from the
// prompt left the test green while no boundary pass could file one. The
// fix is not a longer hardcoded list; it is reading the vocabulary from
// the one place that defines it.
func TestDesignOutcomeTableMatchesTheVocabularies(t *testing.T) {
	blocks, err := promptdoc.Blocks(repoFile(t, "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	table, ok := blocks["agent-outcomes"]
	if !ok {
		t.Fatal("DESIGN.md has no agent-outcomes list — §13's table moved or lost its anchor")
	}

	// Keyed by emitter as well as value, because `pass` and `fail` are
	// each shared between reconcile and the live suite. Keyed on the
	// value alone, a missing live-suite row would hide behind a
	// reconcile row that happens to spell the same word.
	rows := map[string]map[string]string{} // emitter -> value -> label cell
	cell := regexp.MustCompile("`([a-z-]+)`")
	for _, line := range strings.Split(table.Body, "\n") {
		cols := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cols) < 5 || strings.Contains(line, "---") || strings.Contains(line, "emitter") {
			continue
		}
		m := cell.FindStringSubmatch(cols[1])
		if m == nil {
			t.Errorf("row has no value in backticks: %s", line)
			continue
		}
		emitter := strings.TrimSpace(cols[0])
		if rows[emitter] == nil {
			rows[emitter] = map[string]string{}
		}
		rows[emitter][m[1]] = cols[3]
	}
	if len(rows) == 0 {
		t.Fatal("parsed no rows out of the table — the format changed under the parser")
	}

	vocab := map[string][]string{
		"boundary":   protocol.ProposalKinds,
		"abort":      protocol.AbortReasons,
		"design":     protocol.DesignOutcomes,
		"reconcile":  append(append([]string{}, protocol.ReconcileOutcomes...), protocol.CollisionVerdicts...),
		"live suite": protocol.LiveSuiteResults,
	}
	for emitter, values := range vocab {
		for _, v := range values {
			if _, ok := rows[emitter][v]; !ok {
				t.Errorf("the code accepts %q from %s and §13's table never lists it", v, emitter)
			}
		}
		for v := range rows[emitter] {
			if !protocol.Known(values, v) {
				t.Errorf("§13's table lists %q under %s and no vocabulary accepts it", v, emitter)
			}
		}
	}
	for emitter := range rows {
		if _, ok := vocab[emitter]; !ok {
			t.Errorf("§13's table has an emitter %q with no vocabulary behind it", emitter)
		}
	}

	// The label column, for the rows where the mapping is a lookup
	// rather than control flow. Three of the four are not the identity,
	// which is the whole reason the column is worth checking.
	for kind, label := range protocol.ProposalLabels {
		if got := rows["boundary"][kind]; !strings.Contains(got, "`"+label+"`") {
			t.Errorf("kind %q files under %q, and §13's table says %q", kind, label, strings.TrimSpace(got))
		}
	}
}

// A prompt that stops naming a value is a pass that never emits it. The
// vocabulary is not a bare list in any of these prompts — each value
// carries its own paragraph of role-specific argument — so the guard is
// presence, not extraction.
func TestEachRolePromptNamesItsWholeVocabulary(t *testing.T) {
	prompts := promptFiles(t)
	for _, c := range []struct {
		prompt string
		vocab  []string
	}{
		{"boundary.md", protocol.ProposalKinds},
		{"dev.md", []string{"pushback", "needs-setup", "author-only", "scope-satisfied"}},
		{"reconcile.md", protocol.ReconcileOutcomes},
		{"reconcile.md", protocol.CollisionVerdicts},
		{"design.md", protocol.DesignOutcomes},
	} {
		body, ok := prompts[c.prompt]
		if !ok {
			t.Fatalf("no prompts/%s", c.prompt)
		}
		for _, v := range c.vocab {
			if !strings.Contains(body, v) {
				t.Errorf("prompts/%s never names %q, so a pass reading it cannot emit one", c.prompt, v)
			}
		}
	}
}
