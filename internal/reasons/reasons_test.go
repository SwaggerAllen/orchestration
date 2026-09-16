package reasons

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const ported = `---
paths:
  - lib/foundation/**
---

# foundation

## #1 Standing decisions

- **#17 Generated clients are the only door to the backend** — the
  OpenAPI client and the typed channel client.
- **#3 One Repo.** Stores own schemas and queries, never connections.

## #2 Depends on

- engine, for commands.
`

func TestARuleIdIsReadOffAHeadingAndABoldBullet(t *testing.T) {
	d := ParseDoc(ported)
	var got []string
	for _, r := range d.Rules {
		got = append(got, string(r.Kind)+":"+r.ID+"@"+itoa(r.Line)+":"+r.Text)
	}
	want := "heading:1@8:Standing decisions|bullet:17@10:Generated clients are the only door to the backend|bullet:3@12:One Repo.|heading:2@14:Depends on"
	if strings.Join(got, "|") != want {
		t.Errorf("rules = %v\nwant    %s", got, want)
	}
	if len(d.Unnumbered) != 0 {
		t.Errorf("unnumbered = %v, want none", d.Unnumbered)
	}
	if r := d.Rules[1]; r.Raw != "- **#17 Generated clients are the only door to the backend** — the" {
		t.Errorf("Raw = %q", r.Raw)
	}
}

// Catapult's systems/substrate.md carries a bullet whose bold lead wraps
// to the second line before it closes. The id is on the first line, and
// that is the only line the token is looked for on: a pattern demanding
// the closing ** on line one reads the rule as unnumbered, and the audit
// would then fail a doc the port had numbered correctly.
func TestABoldLeadThatWrapsIsReadFromItsFirstLine(t *testing.T) {
	d := ParseDoc("## #1 Standing decisions\n\n- **#42 The apps list that arms that declaration is checked for\n  completeness, by `Catapult.Audit.BoundaryApps`** (ORC-50). The entry\n  above buys compile-grade enforcement.\n")
	if len(d.Rules) != 2 || d.Rules[1].ID != "42" {
		t.Fatalf("rules = %+v, want the container and #42", d.Rules)
	}
	if got := d.Rules[1].Text; got != "The apps list that arms that declaration is checked for completeness, by `Catapult.Audit.BoundaryApps`" {
		t.Errorf("Text = %q", got)
	}
	if len(d.Unnumbered) != 0 {
		t.Errorf("unnumbered = %v", d.Unnumbered)
	}
}

func TestAStandingDecisionWithoutAnIdIsUnnumbered(t *testing.T) {
	d := ParseDoc("## #1 Standing decisions\n\n- **Topology starts `single`** (v5 §2.5 split); the placement\n  discipline is honored.\n- **#4 One Repo.**\n")
	if len(d.Unnumbered) != 1 || d.Unnumbered[0].Kind != KindBullet || d.Unnumbered[0].Line != 3 {
		t.Fatalf("unnumbered = %+v, want the bullet on line 3", d.Unnumbered)
	}
	if got := d.Unnumbered[0].Text; got != "Topology starts `single`" {
		t.Errorf("Text = %q", got)
	}
}

// Screen docs enumerate a decision's parts as bullets — my-queue.md's
// "- **Assigned** (default)" is half of one decision, not a rule — and
// system docs list dependencies as bullets. Only the section that holds
// decisions requires an id on each.
func TestABulletOutsideStandingDecisionsNeedsNoId(t *testing.T) {
	d := ParseDoc("## #1 Two tabs, because they are two questions\n\n- **Assigned** (default) — tickets assigned to you.\n- **My roles** — tickets whose next step is yours.\n\n## #2 Standing decisions\n\n- **#3 Empty is a real state.**\n\n## #4 Depends on\n\n- dashboard, for the queue projection.\n")
	if len(d.Unnumbered) != 0 {
		t.Errorf("unnumbered = %+v, want none — the section closed at ## #4", d.Unnumbered)
	}
	d = ParseDoc("## #1 Standing decisions\n\n- plain bullet\n\n### #2 A sub-heading inside the section\n\n- still inside\n")
	if len(d.Unnumbered) != 2 {
		t.Errorf("unnumbered = %+v, want both bullets: an h3 does not close an h2 section", d.Unnumbered)
	}
}

// The container carries an id too. It costs a token and it leaves the
// attribution rule with no exceptions: a line between the container and
// its first bullet belongs to something.
func TestTheContainerHeadingNeedsAnIdToo(t *testing.T) {
	d := ParseDoc("## Standing decisions\n\n- **#3 One Repo.**\n")
	if len(d.Unnumbered) != 1 || d.Unnumbered[0].Kind != KindHeading || d.Unnumbered[0].Text != "Standing decisions" {
		t.Errorf("unnumbered = %+v, want the container heading", d.Unnumbered)
	}
	if len(d.Rules) != 1 || d.Rules[0].ID != "3" {
		t.Errorf("rules = %+v: the bullet is still read under an unnumbered container", d.Rules)
	}
}

// Front matter is a `- `-item list of globs, and a doc explaining the
// format shows ids inside a fence. Neither is a rule — and a fence is
// masked rather than deleted, so the heading after it is still reported
// on the line it is on.
func TestFrontMatterAndFencesAreNotRules(t *testing.T) {
	doc := "---\npaths:\n  - lib/x/**\n---\n\n# x\n\n```markdown\n## #9 Not a rule\n- **#8 Nor this**\n```\n\n## #1 Standing decisions\n"
	d := ParseDoc(doc)
	if len(d.Rules) != 1 || d.Rules[0].ID != "1" || d.Rules[0].Line != 13 {
		t.Errorf("rules = %+v, want #1 alone, on line 13", d.Rules)
	}
	if len(d.Unnumbered) != 0 {
		t.Errorf("unnumbered = %+v", d.Unnumbered)
	}
}

func TestDuplicateIdsAreReportedWithBothLines(t *testing.T) {
	d := ParseDoc("## #1 Standing decisions\n\n- **#7 A.**\n- **#7 B.**\n\n## #7 Depends on\n")
	if len(d.Duplicates) != 1 || d.Duplicates[0].ID != "7" || len(d.Duplicates[0].Lines) != 3 {
		t.Errorf("duplicates = %+v, want #7 on three lines", d.Duplicates)
	}
}

func TestEveryLineBelongsToTheNearestIdAbove(t *testing.T) {
	d := ParseDoc("# x\n\nIntro belongs to nothing.\n\n## #1 Standing decisions\n\n- **#17 Lead.** Rule sentence.\n\n  A second paragraph of #17.\n- **#3 Next.**\n")
	if got := d.Blocks["17"]; got != "- **#17 Lead.** Rule sentence.\n\n  A second paragraph of #17." {
		t.Errorf("Blocks[17] = %q", got)
	}
	if got := d.Blocks["1"]; got != "## #1 Standing decisions" {
		t.Errorf("Blocks[1] = %q", got)
	}
	if got := d.Blocks["3"]; got != "- **#3 Next.**" {
		t.Errorf("Blocks[3] = %q", got)
	}
	for id, b := range d.Blocks {
		if strings.Contains(b, "Intro belongs") {
			t.Errorf("the preamble was attributed to #%s", id)
		}
	}
}

// The metadata lines are read only before the prose, as the non-asks
// file's scope: line is: a sentence beginning "retired:" partway down a
// reason must not retire the rule. Blank lines between them do not start
// the prose, because `## #17`, `since:`, blank, `retired:` is a shape
// people will write.
func TestAnEntryIsItsIdAndItsMetadataBeforeProse(t *testing.T) {
	f := ParseFile("# foundation — reasons\n\nPreamble is dropped.\n\n## #17\nsince: ORC-22\n\nRevisit: when a second client appears\n\nA hand-written fetch is the client-side sibling of a bypassed LLM call.\nretired: this is prose, not metadata.\n\n## #9\nretired: ORC-90 — the guard moved into the compiler\n")
	if len(f.Entries) != 2 {
		t.Fatalf("entries = %+v", f.Entries)
	}
	e := f.Entries[0]
	if e.ID != "17" || e.Line != 5 || e.Since != "ORC-22" || e.Revisit != "when a second client appears" || e.Retired != "" {
		t.Errorf("entry = %+v", e)
	}
	if !strings.HasPrefix(e.Body, "A hand-written fetch") || !strings.Contains(e.Body, "retired: this is prose") {
		t.Errorf("Body = %q", e.Body)
	}
	if r := f.Entries[1]; !r.IsRetired() || r.Retired != "ORC-90 — the guard moved into the compiler" || r.Body != "" {
		t.Errorf("retired entry = %+v", r)
	}
	if !strings.HasPrefix(f.Blocks["17"], "## #17\nsince: ORC-22") {
		t.Errorf("Blocks[17] = %q", f.Blocks["17"])
	}
}

func TestAnH2ThatIsNotAnIdIsMalformedAndExtendsNoEntry(t *testing.T) {
	f := ParseFile("## #17\n\nThe reason.\n\n## Why the boundary sits here\n\nStray prose.\n\n### #18\n\nA deeper heading is prose inside an entry.\n")
	if len(f.Malformed) != 1 || f.Malformed[0].Line != 5 || f.Malformed[0].Text != "Why the boundary sits here" {
		t.Fatalf("malformed = %+v", f.Malformed)
	}
	if len(f.Entries) != 1 {
		t.Fatalf("entries = %+v, want #17 alone", f.Entries)
	}
	if strings.Contains(f.Entries[0].Body, "Stray") || strings.Contains(f.Blocks["17"], "Stray") {
		t.Errorf("the malformed heading's lines were attributed to #17: %q", f.Blocks["17"])
	}
}

func TestDuplicateEntriesAreReported(t *testing.T) {
	f := ParseFile("## #17\n\nA.\n\n## #17\n\nB.\n")
	if len(f.Duplicates) != 1 || f.Duplicates[0].ID != "17" || len(f.Duplicates[0].Lines) != 2 {
		t.Errorf("duplicates = %+v", f.Duplicates)
	}
}

func TestAReasonsFileNameIsRecognised(t *testing.T) {
	for name, want := range map[string]bool{"foundation.reasons.md": true, "foundation.md": false, "reasons.md": false, "README.md": false} {
		if IsReasonsFile(name) != want {
			t.Errorf("IsReasonsFile(%q) = %v", name, !want)
		}
	}
	if got := ReasonsFileFor("foundation.md"); got != "foundation.reasons.md" {
		t.Errorf("ReasonsFileFor = %q", got)
	}
}

func TestStripIDSeesThroughTheToken(t *testing.T) {
	for in, want := range map[string]string{"#3 States": "States", "States": "States", "#3": "", "#3States": "#3States", " #12  Depends on ": "Depends on"} {
		if got := StripID(in); got != want {
			t.Errorf("StripID(%q) = %q, want %q", in, got, want)
		}
	}
}

// Either signal arms the checks. A doc that gained ids and no sibling
// still has its ids checked for duplicates; a doc that gained a sibling
// first is held to numbering everything.
func TestADocIsPortedByAnIdOrBySibling(t *testing.T) {
	root := t.TempDir()
	write(t, root, "systems/ids.md", "## #1 Standing decisions\n")
	write(t, root, "systems/sibling.md", "## Standing decisions\n")
	write(t, root, "systems/sibling.reasons.md", "# sibling — reasons\n")
	write(t, root, "systems/neither.md", "## Standing decisions\n")
	for name, want := range map[string]bool{"ids": true, "sibling": true, "neither": false} {
		ix, ok, err := Load(root, "systems", name)
		if err != nil || !ok {
			t.Fatalf("Load(%s) = %v, %v", name, ok, err)
		}
		if ix.Ported() != want {
			t.Errorf("%s: Ported = %v, want %v", name, !want, want)
		}
	}
	if _, ok, err := Load(root, "systems", "missing"); ok || err != nil {
		t.Errorf("Load(missing) = %v, %v", ok, err)
	}
}

func TestLoadDirPairsSiblingsAndSkipsReadme(t *testing.T) {
	root := t.TempDir()
	write(t, root, "systems/README.md", "## #1 Not a doc\n")
	write(t, root, "systems/foundation.md", ported)
	write(t, root, "systems/foundation.reasons.md", "## #17\nsince: ORC-22\n\nBecause.\n\n## #9\nretired: ORC-90 — gone\n")
	write(t, root, "systems/engine.md", "## Standing decisions\n")
	ixs, err := LoadDir(root, "systems")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, ix := range ixs {
		names = append(names, ix.Name)
	}
	if strings.Join(names, ",") != "engine,foundation" {
		t.Fatalf("names = %v", names)
	}
	f := ixs[1]
	if f.File == nil || f.Path != "systems/foundation.md" || f.FilePath != "systems/foundation.reasons.md" {
		t.Errorf("foundation = %+v", f)
	}
	if !f.Resolves("17") || !f.Resolves("9") || f.Resolves("99") || !f.Resolves("1") {
		t.Errorf("Resolves: 17=%v 9(retired)=%v 99=%v 1=%v", f.Resolves("17"), f.Resolves("9"), f.Resolves("99"), f.Resolves("1"))
	}
	if got := f.NextID(""); got != "18" {
		t.Errorf("NextID = %q, want 18", got)
	}
	if ixs[0].File != nil || ixs[0].Ported() {
		t.Errorf("engine = %+v, want unported with no sibling", ixs[0])
	}
	if got, err := LoadDir(root, "screens"); got != nil || err != nil {
		t.Errorf("a missing dir = %v, %v", got, err)
	}
}

// Blocks, not rule lines: the prose under a rule is what a pass most
// often rewrites, and a comparison of leads alone would call a rule
// untouched whose whole argument had been replaced.
func TestTouchedIdsAreTheBlocksThatDiffer(t *testing.T) {
	base, head := t.TempDir(), t.TempDir()
	write(t, base, "systems/foundation.md", "## #1 Standing decisions\n\n- **#17 Lead.** Rule.\n\n  Old prose under the rule.\n- **#3 Same.** Unchanged.\n- **#5 Gone.** Removed by the pass.\n")
	write(t, base, "systems/foundation.reasons.md", "## #17\nsince: ORC-22\n\nWhy.\n\n## #9\nretired: ORC-90 — gone\n\n## #3\n\nSame reason.\n")
	write(t, head, "systems/foundation.md", "## #1 Standing decisions\n\n- **#17 Lead.** Rule.\n\n  New prose under the rule.\n- **#3 Same.** Unchanged.\n- **#24 New.** Minted by the pass.\n")
	write(t, head, "systems/foundation.reasons.md", "## #17\nsince: ORC-22\n\nWhy.\n\n## #9\nretired: ORC-90 — gone, and amended\n\n## #3\n\nSame reason.\n")
	// A differing doc that is not in the changed list is not compared.
	write(t, base, "systems/engine.md", "## #1 Standing decisions\n\n- **#2 A.**\n")
	write(t, head, "systems/engine.md", "## #1 Standing decisions\n\n- **#2 B.**\n")

	got, err := TouchedIDs(base, head, []string{"systems/foundation.md", "systems/foundation.reasons.md", "lib/x.ex"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range got {
		ids = append(ids, x.Cite())
	}
	if strings.Join(ids, ",") != "foundation#5,foundation#9,foundation#17,foundation#24" {
		t.Fatalf("touched = %v", ids)
	}
	byID := map[string]Touched{}
	for _, x := range got {
		byID[x.ID] = x
	}
	if x := byID["17"]; x.BaseRule == nil || x.BaseEntry == nil || x.BaseEntry.Since != "ORC-22" || x.New {
		t.Errorf("#17 = %+v, want its base rule and entry", x)
	}
	if x := byID["24"]; !x.New || x.BaseRule != nil || x.BaseEntry != nil {
		t.Errorf("#24 = %+v, want new", x)
	}
	if x := byID["5"]; x.BaseRule == nil || x.BaseEntry != nil || x.New {
		t.Errorf("#5 = %+v, want the base rule and no entry", x)
	}
	if x := byID["9"]; x.BaseRule != nil || x.BaseEntry == nil || !x.BaseEntry.IsRetired() {
		t.Errorf("#9 = %+v, want the retired base entry", x)
	}
}

func TestTouchedIdsReadAMissingBaseFileAsEmpty(t *testing.T) {
	base, head := t.TempDir(), t.TempDir()
	write(t, head, "screens/board.md", "## #1 Two tabs\n\n## #2 Empty is a real state\n")
	got, err := TouchedIDs(base, head, []string{"screens/board.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].New || !got[1].New || got[0].Dir != "screens" {
		t.Errorf("touched = %+v, want two new ids", got)
	}
	got, err = TouchedIDs(base, head, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("no changed files = %v, %v", got, err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// One finding per idle half, in the doc's and the sibling's own words.
func TestAuditReportsEachIdleHalf(t *testing.T) {
	root := t.TempDir()
	write(t, root, "systems/foundation.md", "## Standing decisions\n\n- **Unnumbered lead.** Prose.\n- **#7 A.**\n- **#7 B.**\n- **#9 Still here.**\n- **#3 C.**\n")
	write(t, root, "systems/foundation.reasons.md", "## #17\n\nOrphan.\n\n## Why\n\n## #9\nretired: ORC-90 — gone\n\n## #3\n\nA.\n\n## #3\n\nB.\n")
	ix, _, err := Load(root, "systems", "foundation")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(Audit(ix), "\n")
	for _, want := range []string{
		`systems/foundation.md:1: heading "Standing decisions" carries no id`,
		`systems/foundation.md:3: standing decision "Unnumbered lead." carries no id`,
		`systems/foundation.md: id #7 appears 2 times (lines 4, 5)`,
		`systems/foundation.reasons.md:5: heading "Why" is not an entry`,
		`systems/foundation.reasons.md: entry #3 appears 2 times (lines 10, 14)`,
		`systems/foundation.reasons.md: entry #17 has no rule line in systems/foundation.md and is not retired`,
		`systems/foundation.reasons.md: entry #9 is retired (ORC-90 — gone) but systems/foundation.md still carries rule #9`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "#3 has no rule") {
		t.Errorf("a live entry with a rule line was reported: %s", got)
	}
}

// An id with no entry is not a finding; a retired entry with no rule
// line is the shape retirement takes.
func TestAuditIsCleanOnAWellFormedPair(t *testing.T) {
	root := t.TempDir()
	write(t, root, "systems/foundation.md", ported)
	write(t, root, "systems/foundation.reasons.md", "# foundation — reasons\n\n## #17\nsince: ORC-22\n\nBecause.\n\n## #9\nretired: ORC-90 — gone\n")
	ix, _, err := Load(root, "systems", "foundation")
	if err != nil {
		t.Fatal(err)
	}
	if got := Audit(ix); len(got) != 0 {
		t.Errorf("findings = %v, want none", got)
	}
}

// Measured on Catapult's docs: every `#<digits>` there is a PR number, a
// hex colour or an ordinal, and none puts a doc name before the `#`. The
// whitelist is what keeps them out — a grammar alone would not.
func TestTheCitationGrammarIgnoresWhatCatapultWrites(t *testing.T) {
	root := t.TempDir()
	write(t, root, "systems/foundation.md", ported)
	write(t, root, "lib/x.ex", "# before the fixed harness (ORC-223, PR #144) reached the repo\n# they executed the pre-#144 harness\n# | violet | `#9b8fd4` | and commitment #2, project #1\n# foundation#17 is fine; xfoundation#17 is not a citation; docs/foundation.md#17 neither\n")
	docs, err := LoadDir(root, "systems")
	if err != nil {
		t.Fatal(err)
	}
	got, err := SweepCitations(root, []string{"lib/x.ex"}, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("problems = %v, want none", got)
	}
}

func TestACitationOfARetiredEntryResolvesAndAnUnknownIdDoesNot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "systems/foundation.md", ported)
	write(t, root, "systems/foundation.reasons.md", "## #9\nretired: ORC-90 — gone\n")
	write(t, root, "systems/engine.md", "## Standing decisions\n") // unported: not a citable name
	write(t, root, "lib/x.ex", "# foundation#9 foundation#17 foundation#99 engine#1 system:foundation#3 screen:foundation#3\n")
	docs, err := LoadDir(root, "systems")
	if err != nil {
		t.Fatal(err)
	}
	got, err := SweepCitations(root, []string{"lib/x.ex"}, docs)
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, p := range got {
		s = append(s, p.String())
	}
	joined := strings.Join(s, "\n")
	for _, want := range []string{
		"lib/x.ex:1: cites foundation#99, which is not a rule in systems/foundation.md or an entry in systems/foundation.reasons.md",
		"lib/x.ex:1: cites screen:foundation#3, which names screen:foundation, and there is no ported screens/foundation.md",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if len(got) != 2 {
		t.Errorf("problems = %v, want exactly the two above: #9 is retired and resolves, #17 is a rule, engine is unported, system:foundation#3 is prefixed and resolves", s)
	}
}

func TestAnAmbiguousBareNameIsReportedNotGuessed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "systems/board.md", "## #1 Standing decisions\n")
	write(t, root, "screens/board.md", "## #1 Two tabs\n\n## #2 Empty\n")
	write(t, root, "lib/x.ex", "# board#2 and screen:board#2 and system:board#2\n")
	var docs []Index
	for _, dir := range []string{"systems", "screens"} {
		ixs, err := LoadDir(root, dir)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, ixs...)
	}
	got, err := SweepCitations(root, []string{"lib/x.ex"}, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("problems = %v, want the bare cite (ambiguous) and system:board#2 (no such rule)", got)
	}
	if !strings.Contains(got[0].String(), "is ambiguous: board is both systems/board.md and screens/board.md — write system:board or screen:board") {
		t.Errorf("first = %s", got[0])
	}
	if !strings.Contains(got[1].String(), "cites system:board#2, which is not a rule in systems/board.md, and the doc has no reasons file") {
		t.Errorf("second = %s", got[1])
	}
}

func TestParseCiteReadsOneCitationExactly(t *testing.T) {
	for in, want := range map[string]string{
		"foundation#17":        "/foundation/17",
		"system:foundation#17": "system/foundation/17",
		"screen:my-queue#3":    "screen/my-queue/3",
		"foundation":           "",
		"#17":                  "",
		"foundation#17 more":   "",
		"Foundation#17":        "",
	} {
		prefix, name, id, ok := ParseCite(in)
		got := ""
		if ok {
			got = prefix + "/" + name + "/" + id
		}
		if got != want {
			t.Errorf("ParseCite(%q) = %q, want %q", in, got, want)
		}
	}
}

// A ticket-minted id is the ticket key and a per-ticket counter, read on
// every shape a bare id is — heading, bullet, entry — and a bare ticket
// reference is not one: the counter is required, so `#ORC-247` in a
// heading is an unnumbered heading, not rule "ORC-247".
func TestATicketMintedIdIsReadOnEveryShape(t *testing.T) {
	d := ParseDoc("## #ORC-247-1 Policies\n\n## Standing decisions\n\n- **#ORC-247-2 Applies cites, never re-mints.** Rule.\n- **#17 Bare.** Rule.\n\n## #ORC-247 Not an id\n")
	var got []string
	for _, r := range d.Rules {
		got = append(got, r.ID)
	}
	if strings.Join(got, ",") != "ORC-247-1,ORC-247-2,17" {
		t.Errorf("rules = %v", got)
	}
	if len(d.Unnumbered) != 2 || d.Unnumbered[1].Text != "#ORC-247 Not an id" {
		t.Errorf("unnumbered = %+v, want the container heading and the bare ticket reference", d.Unnumbered)
	}
	f := ParseFile("## #ORC-247-2\nsince: ORC-247\n\nBecause.\n")
	if len(f.Entries) != 1 || f.Entries[0].ID != "ORC-247-2" || f.Entries[0].Since != "ORC-247" {
		t.Errorf("entries = %+v", f.Entries)
	}
	if got := StripID("#ORC-247-1 Policies"); got != "Policies" {
		t.Errorf("StripID = %q", got)
	}
}

// Two tickets designed against one doc from the same main each mint
// "the highest plus one" and collide — Catapult's ORC-246 and ORC-247
// both minted generation#52. Under a ticket the counter is the ticket's
// own, so the next id depends on nothing another branch holds.
func TestNextIDIsScopedToTheMintingTicket(t *testing.T) {
	ix := Index{Doc: ParseDoc("- **#52 A.** x\n- **#ORC-246-1 B.** x\n- **#ORC-246-2 C.** x\n"), File: &File{Entries: []Entry{{ID: "ORC-246-3", Retired: "ORC-250 — undone"}}}}
	for ticket, want := range map[string]string{"": "53", "ORC-246": "ORC-246-4", "ORC-247": "ORC-247-1"} {
		if got := ix.NextID(ticket); got != want {
			t.Errorf("NextID(%q) = %q, want %q", ticket, got, want)
		}
	}
	if h, ok := ix.HighestID("ORC-246"); !ok || h != "ORC-246-3" {
		t.Errorf("HighestID(ORC-246) = %q,%v — a retired entry's number is never reissued", h, ok)
	}
}

// Ids order for reports as numbers under their key, never as strings,
// or #10 sorts before #2 and ORC-247-10 before ORC-247-2.
func TestIdsOrderByKeyThenCounter(t *testing.T) {
	ids := []string{"ORC-247-10", "10", "ORC-246-1", "2", "ORC-247-2"}
	sort.Slice(ids, func(i, j int) bool { return Less(ids[i], ids[j]) })
	if strings.Join(ids, ",") != "2,10,ORC-246-1,ORC-247-2,ORC-247-10" {
		t.Errorf("sorted = %v", ids)
	}
	for in, want := range map[string]bool{"17": true, "ORC-247-2": true, "ORC-247": false, "orc-247-2": false, "#17": false, "17a": false} {
		if got := ValidID(in); got != want {
			t.Errorf("ValidID(%q) = %v", in, got)
		}
	}
	if Ticket("ORC-247-2") != "ORC-247" || Ticket("17") != "" || Seq("ORC-247-2") != 2 || Seq("17") != 17 {
		t.Errorf("Ticket/Seq misread: %q %q %d %d", Ticket("ORC-247-2"), Ticket("17"), Seq("ORC-247-2"), Seq("17"))
	}
}

// The citation grammar reads a ticket-minted id wherever it read a bare
// one, and still reads nothing where nothing was cited: a ticket named
// after a doc with no counter, and the forms the bare grammar excluded.
func TestTheCitationGrammarReadsATicketMintedId(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "systems/generation.md", "## #1 Generation\n\n## Standing decisions\n\n- **#ORC-247-2 Applies.** x\n- **#17 Bare.** x\n")
	write(t, dir, "lib/x.ex", "# generation#ORC-247-2 and generation#17 resolve; generation#ORC-247-9 and generation#99 do not; generation#ORC-247 is a ticket, PR #144 a PR, #17ff00 a colour\n")
	docs, err := LoadDir(dir, "systems")
	if err != nil {
		t.Fatal(err)
	}
	got, err := SweepCitations(dir, []string{"lib/x.ex"}, docs)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "ORC-247-9,99" {
		t.Errorf("dangling = %v, want exactly the two that resolve to nothing", ids)
	}
	if _, _, id, ok := ParseCite("system:generation#ORC-247-2"); !ok || id != "ORC-247-2" {
		t.Errorf("ParseCite = %q,%v", id, ok)
	}
	if _, _, _, ok := ParseCite("generation#ORC-247"); ok {
		t.Error("a ticket reference with no counter parsed as a citation")
	}
}

func TestMentionsRuleID(t *testing.T) {
	for _, yes := range []string{
		"## #17 One repo",
		"- **#17 One Repo.** Stores own schemas.",
		"the rule this contradicts is `engine#17`",
		"## #ORC-247-2 A ticket's own id",
		"see (#42) for the reason",
	} {
		if !MentionsRuleID(yes) {
			t.Errorf("missed a rule id in %q", yes)
		}
	}
	for _, no := range []string{
		"a plain heading",
		"issue #foo is not an id",
		"## Standing decisions",
		"a colour like #ff00aa",
		"",
	} {
		if MentionsRuleID(no) {
			t.Errorf("found a rule id in %q", no)
		}
	}
}
