package reasons

import (
	"fmt"
	"strings"
	"testing"
)

const declaring = `# widget

## #1 The file

- **#2 A widget names itself.** Prose.
  - key ` + "`name`" + `: string, required
  - key ` + "`parts`" + `: map, optional
  - key ` + "`parts.*`" + `: map
  - key ` + "`parts.*.mode`" + `: "eventual" | "transactional", optional

## #3 Steps

- key ` + "`extra`" + `: boolean, optional
`

func TestADeclarationBelongsToTheNearestIdAbove(t *testing.T) {
	d := ParseDoc(declaring)
	if len(d.BadDeclarations) != 0 {
		t.Fatalf("bad = %+v", d.BadDeclarations)
	}
	var got []string
	for _, x := range d.Declarations {
		got = append(got, x.Path+"@"+x.RuleID+":"+strings.Join(x.Types, "|")+":"+x.Presence)
	}
	want := `name@2:string:required|parts@2:map:optional|parts.*@2:map:|parts.*.mode@2:"eventual"|"transactional":optional|extra@3:boolean:optional`
	if strings.Join(got, "|") != want {
		t.Errorf("declarations = %s\nwant           %s", strings.Join(got, "|"), want)
	}
	if x := d.Declarations[0]; x.Line != 6 || x.Raw != "  - key `name`: string, required" {
		t.Errorf("first = line %d raw %q", x.Line, x.Raw)
	}
}

// A declaration written at the top level under Standing decisions is a
// bullet with no bold id, which is exactly what the unnumbered check
// looks for. It is a declaration, and reading it as a decision missing
// its id would make every top-level declaration a finding.
func TestADeclarationIsNotAStandingDecisionMissingItsId(t *testing.T) {
	d := ParseDoc("## #1 Standing decisions\n\n- **#2 Rule.**\n- key `a`: string, optional\n")
	if len(d.Unnumbered) != 0 {
		t.Errorf("unnumbered = %+v, want none", d.Unnumbered)
	}
	if len(d.Declarations) != 1 || d.Declarations[0].RuleID != "2" {
		t.Errorf("declarations = %+v", d.Declarations)
	}
}

func TestADeclarationInAFenceIsAnExampleNotADeclaration(t *testing.T) {
	d := ParseDoc("## #1 Rule\n\n```markdown\n- key `a`: string, optional\n```\n")
	if len(d.Declarations) != 0 || len(d.BadDeclarations) != 0 {
		t.Errorf("declarations = %+v bad = %+v", d.Declarations, d.BadDeclarations)
	}
}

func TestProseThatSaysKeyIsNotADeclarationAttempt(t *testing.T) {
	d := ParseDoc("## #1 Rule\n\n- key point: nothing here is a path\n  - key insight, also prose\n")
	if len(d.Declarations) != 0 || len(d.BadDeclarations) != 0 {
		t.Errorf("declarations = %+v bad = %+v", d.Declarations, d.BadDeclarations)
	}
}

func TestAMalformedDeclarationSaysWhatToFix(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{"- key `a` string", "the form is"},
		{"- key ``: string", "the path is empty"},
		{"- key `a..b`: string", `segment "" of`},
		{"- key `a.$b`: map", "names shape $b after its first segment"},
		{"- key `a`:", "has no type"},
		{"- key `a`: strng", `type "strng" is not string`},
		{"- key `a`: string, required, optional", "states its presence twice"},
		{"- key `a`: string, mandatory", `clause "mandatory" is not required`},
		{"- key `a`: string, required, when k is queue", "when value queue is not \"quoted\""},
		{"- key `a`: \"open, required", "opens a quote it does not close"},
		{"- key `a`: string, when k is \"x\", when j is \"y\"", "two when clauses — join conditions with and"},
		{"- key `a`: string, optional, when k is \"x\" and k is not \"y\"", "when names `k` twice"},
		{"- key `a`: string, optional, when k equals \"x\"", `condition "k equals \"x\"" is not`},
		{"- key `a`: string, optional, when k is \"x\" and", `condition "" is not`},
	} {
		d := ParseDoc("## #1 Rule\n\n" + c.line + "\n")
		if len(d.BadDeclarations) != 1 || !strings.Contains(d.BadDeclarations[0].Why, c.want) {
			t.Errorf("%s: bad = %+v, want a reason containing %q", c.line, d.BadDeclarations, c.want)
		}
	}
}

func TestAPathSplitsListItemsIntoTheirOwnSegments(t *testing.T) {
	d := ParseDoc("## #1 Rule\n\n- key `types.*.statuses[][]`: $entry\n")
	if len(d.Declarations) != 1 {
		t.Fatalf("bad = %+v", d.BadDeclarations)
	}
	x := d.Declarations[0]
	if strings.Join(x.Segs, " ") != "types * statuses [] []" || x.Path != "types.*.statuses[][]" || x.Parent() != "types.*.statuses[]" {
		t.Errorf("segs = %q path = %q parent = %q", x.Segs, x.Path, x.Parent())
	}
}

func check(t *testing.T, body string) []string {
	t.Helper()
	return CheckDeclarations(Index{Path: "docs/dsl/widget.md", Name: "widget", Doc: ParseDoc(body)})
}

func TestAWellFormedSetHasNoFindings(t *testing.T) {
	body := declaring + `
- **#4 Steps are entries or groups of them.**
  - key ` + "`steps`" + `: list, required
  - key ` + "`steps[]`" + `: $entry | list
  - key ` + "`steps[][]`" + `: $entry
  - key ` + "`$entry`" + `: map
  - key ` + "`$entry.status`" + `: "queue" | "plain", required
  - key ` + "`$entry.flow`" + `: string, required, when status is "queue"
`
	if got := check(t, body); len(got) != 0 {
		t.Errorf("findings = %q", got)
	}
}

// Each finding the compiler refuses on, one declaration set apiece. The
// rule line is there so attribution never confounds the case.
func TestEachMalformedSetIsReportedWithItsReason(t *testing.T) {
	for _, c := range []struct{ name, decls, want string }{
		{"duplicate", "- key `a`: string, optional\n- key `a`: string, optional", "declares `a` 2 times (lines 3, 4)"},
		{"parent undeclared", "- key `a.b`: string, optional", "`a.b` sits in `a`, which is not declared"},
		{"items under a non-list", "- key `a`: map, optional\n- key `a[]`: string", "`a[]` declares list items, but `a` is map, not a list"},
		{"member under a non-map", "- key `a`: list, optional\n- key `a.b`: string, optional", "`a.b` declares a map member, but `a` is list, not a map"},
		{"member under a shape", "- key `$s`: map\n- key `a`: $s, optional\n- key `a.b`: string, optional", "declare the member on $s itself"},
		{"presence missing", "- key `a`: string", "`a` is a named member and states neither required nor optional"},
		{"presence on values", "- key `a`: map, optional\n- key `a.*`: string, required", "`a.*` is a map's values and cannot be required"},
		{"presence on a shape", "- key `$s`: map, optional\n- key `a`: $s, optional", "`$s` is a shape and cannot be optional"},
		{"gate on values", "- key `a`: map, optional\n- key `k`: string, optional\n- key `a.*`: string, when k is \"x\"", "`a.*` is a map's values and cannot be gated"},
		{"gate on no sibling", "- key `a`: string, optional, when k is \"x\"", "`a` is gated on `k`, which is not a declared sibling"},
		{"gate on an impossible value", "- key `k`: \"x\" | \"y\", optional\n- key `a`: string, optional, when k is \"z\"", "`a` is gated on `k` being \"z\", which `k` cannot hold"},
		{"negated impossible value", "- key `k`: \"x\" | \"y\", optional\n- key `a`: string, optional, when k is not \"z\"", "`a` is gated on `k` not being \"z\", which `k` cannot hold"},
		{"second condition's sibling undeclared", "- key `k`: string, optional\n- key `a`: string, optional, when k is \"x\" and j is not \"y\"", "`a` is gated on `j`, which is not a declared sibling"},
		{"gate on a non-string", "- key `k`: integer, optional\n- key `a`: string, optional, when k is \"1\"", "`a` is gated on `k`, which is integer and holds no value"},
		{"shape undeclared", "- key `a`: $s, optional", "`a` is of shape $s, which this doc does not declare"},
		{"shape unused", "- key `$s`: map", "declares shape `$s` and no attribute is of it"},
		{"star beside a gate", "- key `k`: string, optional\n- key `*`: string\n- key `a`: string, optional, when k is \"x\"", "the file's root declares both `*` and a member gated by when"},
		{"above every id", "", "declares `z` above the doc's first rule id"},
		{"not a declaration", "- key `a`: strng, optional", "is not a declaration: type \"strng\""},
	} {
		body := "## #1 Rule\n\n" + c.decls + "\n"
		if c.name == "above every id" {
			body = "- key `z`: string, optional\n\n## #1 Rule\n"
		}
		got := check(t, body)
		if !strings.Contains(strings.Join(got, "\n"), c.want) {
			t.Errorf("%s: findings = %q\nwant one containing %q", c.name, got, c.want)
		}
	}
}

func TestADeclarationArmsTheAudit(t *testing.T) {
	ix := Index{Path: "docs/dsl/widget.md", Doc: ParseDoc("- key `a`: string, optional\n")}
	if !ix.Ported() {
		t.Fatal("a doc with a declaration and no ids must be held to the audit, or its unattributed declaration is never reported")
	}
	if got := Audit(ix); !strings.Contains(strings.Join(got, "\n"), "above the doc's first rule id") {
		t.Errorf("audit = %q", got)
	}
}

// The attribution rule is what makes a declaration part of its rule, so
// changing one must hand the record review that rule — the property the
// whole design rests on, asserted rather than assumed.
func TestChangingADeclarationTouchesItsRule(t *testing.T) {
	base, head := t.TempDir(), t.TempDir()
	write(t, base, "docs/dsl/widget.md", declaring)
	write(t, head, "docs/dsl/widget.md", strings.Replace(declaring, "`parts.*.mode`: \"eventual\" | \"transactional\"", "`parts.*.mode`: \"eventual\"", 1))
	got, err := TouchedIDs(base, head, []string{"docs/dsl/widget.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Cite() != "widget#2" {
		t.Errorf("touched = %+v, want widget#2 alone", got)
	}
}

func TestAKeyReferenceResolvesAgainstItsDocsDeclarations(t *testing.T) {
	root := t.TempDir()
	write(t, root, "docs/dsl/widget.md", declaring)
	write(t, root, "docs/dsl/bare.md", "## #1 Rule\n")
	write(t, root, "systems/widget.md", "## #1 Heading\n")
	write(t, root, "lib/x.ex", "# widget@ means nothing alone; mail someone@example.com\n"+
		"# dsl:widget@parts.*.mode is fine and dsl:widget@parts.*.colour is not\n"+
		"# widget@name is ambiguous; bare@name has nothing declared\n")
	var docs []Index
	for _, dir := range []string{"docs/dsl", "systems"} {
		ixs, err := LoadDir(root, dir)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, ixs...)
	}
	got, err := SweepKeyRefs(root, []string{"lib/x.ex"}, docs)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, p := range got {
		lines = append(lines, p.String())
	}
	want := []string{
		"lib/x.ex:2: refers to dsl:widget@parts.*.colour, which is not an attribute docs/dsl/widget.md declares",
		"lib/x.ex:3: refers to widget@name, which is ambiguous: widget is both docs/dsl/widget.md and systems/widget.md — write dsl:widget or system:widget",
		"lib/x.ex:3: refers to bare@name, which is not declared: docs/dsl/bare.md declares no attributes",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("problems =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestParseDocNameTakesEveryKindsPrefix(t *testing.T) {
	for in, want := range map[string]string{"chain": "|chain", "dsl:chain": "dsl|chain", "system:billing": "system|billing"} {
		prefix, name, ok := ParseDocName(in)
		if !ok || prefix+"|"+name != want {
			t.Errorf("ParseDocName(%q) = %q %q %v", in, prefix, name, ok)
		}
	}
	if _, _, ok := ParseDocName("widget:chain"); ok {
		t.Error("an unknown prefix must not parse")
	}
}

func TestAWhenReadsNegationAndConjunction(t *testing.T) {
	d := ParseDoc("## #1 Rule\n\n- key `a`: string, optional, when generator is not \"supplied\" and draft is not \"none\" | \"x and y\"\n")
	if len(d.Declarations) != 1 {
		t.Fatalf("bad = %+v", d.BadDeclarations)
	}
	var got []string
	for _, c := range d.Declarations[0].When {
		got = append(got, fmt.Sprintf("%s not=%v %q", c.Sibling, c.Not, c.Values))
	}
	// The "and" inside the quoted value is a value, not a conjunction.
	want := `generator not=true ["supplied"]|draft not=true ["none" "x and y"]`
	if strings.Join(got, "|") != want {
		t.Errorf("conditions = %s\nwant         %s", strings.Join(got, "|"), want)
	}
}

// Two discriminators may gate each other. A condition tests a sibling's
// value, never whether the sibling was itself allowed, so the pair is well
// defined — and it is what Catapult's tiers need: `generator` exists while
// `draft is not "none"`, and `draft` while `generator is not "supplied"`.
func TestDiscriminatorsMayGateEachOther(t *testing.T) {
	body := "## #1 Rule\n\n" +
		"- key `generator`: string, optional, when draft is not \"none\"\n" +
		"- key `draft`: map | \"none\", optional, when generator is not \"supplied\"\n"
	if got := check(t, body); len(got) != 0 {
		t.Errorf("findings = %q", got)
	}
}
