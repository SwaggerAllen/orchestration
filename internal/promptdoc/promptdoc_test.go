package promptdoc

import (
	"strings"
	"testing"
)

const doc = `# Protocol

7. **Debt scan**, bounded inputs only.

   <!-- pipeline:list id=debt-scan-inputs -->
   - Diffs merged since the last boundary.
   - New TODO markers.
     - and a nested one
   <!-- /pipeline:list -->

Prose after.

<!-- pipeline:list id=design-artifacts -->
- A component.
<!-- /pipeline:list -->
`

func TestBlocksReadsAnchoredLists(t *testing.T) {
	got, err := Blocks(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d blocks, want 2: %v", len(got), got)
	}
	if strings.Contains(got["debt-scan-inputs"].Body, "Prose after") {
		t.Error("the block swallowed text past its close")
	}
}

// Anchors have to work where the content belongs, and in DESIGN that is
// often inside a numbered step. Carried into a prompt with its three
// spaces intact, a bullet list renders as a code block — the bounded
// inputs would appear in a grey box and read as an example rather than
// as the rule.
func TestBlocksDedentsWithoutFlatteningNesting(t *testing.T) {
	got, _ := Blocks(doc)
	lines := strings.Split(got["debt-scan-inputs"].Body, "\n")
	if strings.HasPrefix(lines[0], " ") {
		t.Errorf("the block kept its outer indentation: %q", lines[0])
	}
	if !strings.HasPrefix(lines[2], "  - and a nested") {
		t.Errorf("dedent flattened the nested bullet: %q", lines[2])
	}
}

func TestExpandSubstitutesAndCarriesTheTrail(t *testing.T) {
	blocks, _ := Blocks(doc)
	out, err := Expand("Look at these:\n\n<!-- pipeline:include debt-scan-inputs (DESIGN §10 step 7) -->\n\nThen stop.\n", blocks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Diffs merged since the last boundary") {
		t.Errorf("the list did not land:\n%s", out)
	}
	// The trail travels into the assembled prompt, not just the file, so
	// whoever reads a run's prompt can see it came from the protocol.
	if !strings.Contains(out, "_DESIGN §10 step 7_") {
		t.Errorf("the trail back to DESIGN was dropped:\n%s", out)
	}
	if !strings.Contains(out, "Then stop.") {
		t.Errorf("surrounding prose was lost:\n%s", out)
	}
}

// The whole contract. An include that rendered as nothing would delete a
// rule from a prompt silently, and a pass missing a rule does not report
// that it is missing one — it does the wrong thing and says it went fine.
func TestExpandFailsClosedOnAnUnknownInclude(t *testing.T) {
	out, err := Expand("<!-- pipeline:include no-such-list -->\n", map[string]Block{})
	if err == nil {
		t.Fatalf("an unresolved include produced a prompt: %q", out)
	}
	if !strings.Contains(err.Error(), "no-such-list") {
		t.Errorf("the error must name the anchor, got %v", err)
	}
}

// An empty list is the failure this package exists to prevent, arriving
// by another route: an empty bounded-input list turns the debt scan into
// the open-ended question DESIGN §10 says produces invented findings.
// A `for=tests` block is stated once and asserted, never inlined —
// §13's outcome table spans four emitters where any one pass needs a
// third of it, so including it everywhere would be the bloat this repo
// keeps removing. The distinction lives in the document, not in a list
// inside the checker, or the checker grows the copy the mechanism exists
// to delete.
func TestBlocksMarksReferenceOnlyLists(t *testing.T) {
	got, err := Blocks("<!-- pipeline:list id=a for=tests -->\n- one\n<!-- /pipeline:list -->\n<!-- pipeline:list id=b -->\n- two\n<!-- /pipeline:list -->\n")
	if err != nil {
		t.Fatal(err)
	}
	if !got["a"].Reference {
		t.Error("for=tests did not mark the block reference-only")
	}
	if got["b"].Reference {
		t.Error("a plain list was marked reference-only")
	}
}

func TestBlocksRefusesMalformedAnchors(t *testing.T) {
	for name, body := range map[string]string{
		"empty":       "<!-- pipeline:list id=a -->\n\n<!-- /pipeline:list -->\n",
		"unclosed":    "<!-- pipeline:list id=a -->\n- one\n",
		"duplicate":   "<!-- pipeline:list id=a -->\n- one\n<!-- /pipeline:list -->\n<!-- pipeline:list id=a -->\n- two\n<!-- /pipeline:list -->\n",
		"nested":      "<!-- pipeline:list id=a -->\n<!-- pipeline:list id=b -->\n- one\n<!-- /pipeline:list -->\n",
		"close-alone": "- one\n<!-- /pipeline:list -->\n",
	} {
		if _, err := Blocks(body); err == nil {
			t.Errorf("%s: accepted a malformed document", name)
		}
	}
}
