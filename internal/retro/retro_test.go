package retro

import (
	"reflect"
	"strings"
	"testing"
)

// The note is written once and read twice, by two readers that never
// meet — so the contract that matters is that what Render writes, Parse
// reads back unchanged.
func TestRenderParseRoundTrip(t *testing.T) {
	in := []Entry{
		{Key: "ORC-23", Title: "Add a farewell to the home screen", SHAs: []string{"0655929c405d57bc77b341c476267e45472e3985"}},
		// Closed without landing anything — most boundaries archive a few.
		{Key: "ORC-9", Title: "Decide the greeting copy"},
		// Merged, reverted by hand, merged again. Both shas are the
		// reset's business: reverting only the newer one leaves half the
		// ticket on main.
		{Key: "ORC-18", Title: "Add a farewell", SHAs: []string{
			"9c26c2857bb89998f12862e5cfef9f6c2d5da009",
			"fd93b7210e25acabd772d58b696193aba9670241",
		}},
		// A title carrying the punctuation the format itself uses. The
		// merge clause is anchored to end-of-line and hex-only precisely
		// so this cannot be misread as one.
		{Key: "ORC-4", Title: "Rename greet/2 (merged into greet/1) — see systems/greetings.md"},
	}
	note := Render("Rehearsal 1", in)
	got := Parse(note)

	want := map[string]Entry{}
	for _, e := range in {
		want[e.Key] = e
	}
	if len(got) != len(in) {
		t.Fatalf("parsed %d entries from %d rendered:\n%s", len(got), len(in), note)
	}
	for _, e := range got {
		if !reflect.DeepEqual(e, want[e.Key]) {
			t.Errorf("%s round-tripped as %+v, want %+v", e.Key, e, want[e.Key])
		}
	}
}

// Notes written before the archive step recorded shas are still in the
// repos this reads, and they parse as what they honestly are: the ticket
// happened, and this note cannot say what it landed.
func TestParseReadsANoteWrittenWithoutSHAs(t *testing.T) {
	note := "# Retro — Rehearsal 1\n\nShipped (archived from the tracker):\n\n- ORC-23 — Add a farewell to the home screen\n"
	got := Parse(note)
	if len(got) != 1 || got[0].Key != "ORC-23" || len(got[0].SHAs) != 0 {
		t.Fatalf("got %+v, want the entry with no shas", got)
	}
}

// Prose around the list is expected — a human or a model may annotate a
// retro — and must not take the entries with it. Loose on purpose in the
// other direction too: a prose bullet that happens to look like an entry
// is read as one, because it carries no sha and so costs nothing, while
// a stricter pattern that missed a real entry would cost exactly the
// silence this whole change is about.
func TestParseKeepsTheEntriesAndTakesNoSHAsFromProse(t *testing.T) {
	note := strings.Join([]string{
		"# Retro — M: alpha",
		"",
		"Shipped:",
		"",
		"- ORC-1 — A thing (merged 0655929c405d57bc77b341c476267e45472e3985)",
		"",
		"Notes from the pass:",
		"",
		"- the live suite reported no-tests, which is not a failure",
		"- see docs/retros/m-zero.md for the previous milestone",
		"",
	}, "\n")
	got := Parse(note)

	shas := []string{}
	for _, e := range got {
		shas = append(shas, e.SHAs...)
	}
	if len(shas) != 1 || shas[0] != "0655929c405d57bc77b341c476267e45472e3985" {
		t.Fatalf("shas = %v, want exactly the one merge in the note", shas)
	}
	for _, e := range got {
		if e.Key == "ORC-1" && e.Title != "A thing" {
			t.Errorf("title = %q, want the merge clause stripped off it", e.Title)
		}
	}
}

// Sorted output, so two boundaries over the same set of tickets write
// the same file whatever order the tracker listed them in.
func TestRenderSortsEntries(t *testing.T) {
	note := Render("M: alpha", []Entry{{Key: "ORC-9", Title: "b"}, {Key: "ORC-1", Title: "a"}})
	if strings.Index(note, "ORC-1") > strings.Index(note, "ORC-9") {
		t.Errorf("entries not sorted:\n%s", note)
	}
}
