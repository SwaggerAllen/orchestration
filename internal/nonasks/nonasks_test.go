package nonasks

import (
	"strings"
	"testing"
)

// A deliberately mixed fixture, not a model of a healthy file. Under
// the placement rule (DESIGN §4) a single-doc scope means the entry
// belongs in that doc, so `screen:roster` here is a misplaced entry —
// which is exactly why selection has to keep handling it. Misplaced
// entries exist in real files, and the parser's job is to select them
// correctly, not to have opinions about where they should live.
const doc = `# Confirmed non-asks

Everything here is something the author decided against.

## No dark mode
scope: universal

Two palettes, one designer.

## No client-side validation on the cap form
scope: screen:cap, system:billing

The server is the only authority; a second copy of the rules drifts.

## No pagination in the roster
scope: screen:roster

Thirty rows is the ceiling the product has.

## No background job runner

Nobody scoped this one.
`

func titles(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Title)
	}
	return out
}

func TestParseReadsHeadingsScopesAndProse(t *testing.T) {
	got := Parse(doc)
	if len(got) != 4 {
		t.Fatalf("parsed %d entries: %v", len(got), titles(got))
	}
	if !got[0].IsUniversal() {
		t.Errorf("an explicit `scope: universal` did not parse as universal: %v", got[0].Scope)
	}
	if want := []string{"screen:cap", "system:billing"}; strings.Join(got[1].Scope, ",") != strings.Join(want, ",") {
		t.Errorf("scope = %v, want %v", got[1].Scope, want)
	}
	if !strings.Contains(got[1].Body, "only authority") || strings.Contains(got[1].Body, "scope:") {
		t.Errorf("body carries the scope line or lost its prose: %q", got[1].Body)
	}
	// The preamble is not a refusal, and repeating it in every prompt is
	// what this package exists to stop.
	for _, e := range got {
		if strings.Contains(e.Body, "Everything here is something") {
			t.Errorf("the document's preamble was parsed as an entry: %q", e.Title)
		}
	}
}

// The migration path, and the reason no project has to be converted
// before this ships: today's flat files behave exactly as they do now.
func TestParseTreatsAnUnscopedFileAsOneUniversalEntry(t *testing.T) {
	got := Parse("- No dark mode: two palettes, one designer.\n- No offline mode.\n")
	if len(got) != 1 || !got[0].IsUniversal() {
		t.Fatalf("got %d entries, universal=%v", len(got), len(got) > 0 && got[0].IsUniversal())
	}
	if !strings.Contains(got[0].Body, "No dark mode") || !strings.Contains(got[0].Body, "No offline mode") {
		t.Errorf("the file's content did not survive: %q", got[0].Body)
	}
}

// The one failure mode worth designing against. An unseen refusal gets
// re-proposed, which is the whole thing this document exists to prevent
// — so an entry nobody scoped goes to everybody.
func TestSelectNeverDropsAnUnscopedEntry(t *testing.T) {
	got := Select(Parse(doc), nil, "")
	if len(got) != 2 {
		t.Fatalf("selected %v, want the two universal entries", titles(got))
	}
	for _, want := range []string{"No dark mode", "No background job runner"} {
		found := false
		for _, e := range got {
			if e.Title == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q was hidden from a pass that carries no labels", want)
		}
	}
}

func TestSelectMatchesTheTicketsLabels(t *testing.T) {
	got := titles(Select(Parse(doc), []string{"screen:roster"}, ""))
	if strings.Join(got, "|") != "No dark mode|No pagination in the roster|No background job runner" {
		t.Errorf("selected %v", got)
	}
}

// A first design pass carries no mutex labels, because the design pass
// is what creates them — so label matching alone would show design
// nothing but the universal set, and design is the pass this document is
// written for.
func TestSelectMatchesANameInTheTicketText(t *testing.T) {
	got := titles(Select(Parse(doc), nil, "Cap screen: show cap_reached when the limit is hit"))
	found := false
	for _, ti := range got {
		if ti == "No client-side validation on the cap form" {
			found = true
		}
	}
	if !found {
		t.Errorf("a ticket about the cap screen was not shown the cap refusal: %v", got)
	}
	// And it stays a filter rather than becoming a pass-through.
	for _, ti := range got {
		if ti == "No pagination in the roster" {
			t.Errorf("an unrelated refusal was selected: %v", got)
		}
	}
}

func TestSelectIsCaseInsensitive(t *testing.T) {
	got := titles(Select(Parse(doc), []string{"Screen:Roster"}, ""))
	for _, ti := range got {
		if ti == "No pagination in the roster" {
			return
		}
	}
	t.Errorf("a label differing only in case did not match: %v", got)
}

// A sentence starting "scope:" partway down an entry must not silently
// rescope the refusal above it.
func TestParseIgnoresAScopeLineInsideTheProse(t *testing.T) {
	got := Parse("## No dark mode\nscope: screen:cap\n\nWe ruled it out.\nscope: this reads like a field but is prose.\n")
	if len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	if strings.Join(got[0].Scope, ",") != "screen:cap" {
		t.Errorf("scope = %v, want only the leading line to count", got[0].Scope)
	}
	if !strings.Contains(got[0].Body, "reads like a field") {
		t.Errorf("the prose line was eaten as a field: %q", got[0].Body)
	}
}

// A selected slice has to render back as the document it came from, or
// the prompt needs a second format to explain.
func TestRenderRoundTrips(t *testing.T) {
	first := Parse(doc)
	again := Parse(Render(first))
	if len(again) != len(first) {
		t.Fatalf("round trip: %d entries in, %d out", len(first), len(again))
	}
	for i := range first {
		if again[i].Title != first[i].Title || again[i].Body != first[i].Body {
			t.Errorf("entry %d changed: %+v -> %+v", i, first[i], again[i])
		}
		if strings.Join(again[i].Scope, ",") != strings.Join(first[i].Scope, ",") {
			t.Errorf("entry %d scope changed: %v -> %v", i, first[i].Scope, again[i].Scope)
		}
	}
}

// The shape a project starts with: the file is created in the bootstrap
// commit, before anything has been refused. It must not parse as one
// refusal whose text is the document's title.
func TestParseTreatsATitleOnlyFileAsNoEntries(t *testing.T) {
	for _, body := range []string{
		"# Confirmed non-asks\n",
		"\n# Confirmed non-asks\n\n",
		"",
		"   \n",
	} {
		if got := Parse(body); len(got) != 0 {
			t.Errorf("Parse(%q) = %d entries (%v), want none", body, len(got), titles(got))
		}
	}
}

// But a legacy flat file under that same title keeps every word of it.
func TestParseKeepsAFlatFilesContentUnderItsTitle(t *testing.T) {
	got := Parse("# Confirmed non-asks\n\n- No dark mode: two palettes, one designer.\n")
	if len(got) != 1 || !got[0].IsUniversal() {
		t.Fatalf("got %d entries", len(got))
	}
	if !strings.Contains(got[0].Body, "No dark mode") {
		t.Errorf("content lost: %q", got[0].Body)
	}
	if strings.Contains(got[0].Body, "Confirmed non-asks") {
		t.Errorf("the document title was carried in as a refusal: %q", got[0].Body)
	}
}
