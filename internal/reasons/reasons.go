// Package reasons is the rationale index: the join between a rule stated
// in a screen or system doc and the reason it holds, recorded beside it
// (DESIGN §4).
//
// A rule and its reason have different lifetimes. The rule is what every
// design pass reads; the reason is what one pass reads, the pass that is
// about to change the rule. Inline, the two are paid for together on
// every read — measured on Catapult's tree, the `## Standing decisions`
// sections grew from 388 KB to 521 KB in the week to 2026-09-01 and
// stood at 753 KB across 341 bullets on 2026-09-09, with the bold leads
// that state the rules at 4.4% of that text. Moved out
// without a join, the reason is the half a later pass loses: Catapult's
// `docs/conventions.md` §12 records a commit that moved reasoning "into
// the code it guards" and landed one half of it nowhere.
//
// So the join is the point, and it is mechanical at three places. An id
// names a rule: `## #3 Heading` on a heading, `- **#17 Lead…**` at the
// head of a bullet's bold lead, unique within its doc and never reused or
// renumbered. A sibling file — `systems/foundation.reasons.md` beside
// `systems/foundation.md` — holds an entry per id, `## #17`, its metadata
// lines (`since:`, `revisit:`, `retired:`) before any prose, then the
// reason. And every line in a doc belongs to the nearest id above it,
// which is what lets a diff say which rules it touched without anyone
// deciding where a rule ends.
//
// Per-doc rather than global ids, because the mutex (DESIGN §6) already
// holds one ticket per doc, so a per-doc counter cannot race; two tickets
// on two docs minting from one global counter could mint the same next
// number in parallel.
//
// Entries are optional. A screen section is a description as often as it
// is a rule, and a check demanding an entry for every id would be
// satisfied with a placeholder sentence — the kind of line the paring
// pass just removed. An id with no entry answers "no reason is recorded",
// and that answer reaching a reviewer is itself information.
//
// A retired rule loses its line and keeps its entry, with a `retired:`
// line saying which ticket stopped it and why. That is what keeps the id
// from being reused, and it keeps the one fact a later pass most needs:
// that this was tried, and why it was undone.
//
// This package imports the standard library only. The doc-map loader,
// the doc lint and the decisions index all import it to recognise a
// reasons file and to see through an id token, and the decisions index
// already imports nonasks — an edge back from here would cycle.
package reasons

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kind is which shape an id-bearing line has.
type Kind string

const (
	// KindHeading is an h2–h6 heading: `## #3 Cross-project, deliberately`.
	KindHeading Kind = "heading"
	// KindBullet is a top-level bullet whose bold lead opens with the
	// token: `- **#17 Generated clients are the only door** …`.
	KindBullet Kind = "bullet"
)

// Rule is one id-bearing line in a doc.
type Rule struct {
	ID   int
	Kind Kind
	// Line is 1-based, in the file as read: fences and front matter are
	// masked rather than removed, so a line number here is the file's.
	Line int
	// Text is the heading after its token, or the bullet's bold lead
	// joined across the lines it wraps over.
	Text string
	// Raw is the physical first line, as it stands.
	Raw string
}

// Unnumbered is a heading, or a bullet under `## Standing decisions`,
// that carries no id. In a ported doc each is an audit finding.
type Unnumbered struct {
	Kind Kind
	Line int
	Text string
}

// Dup is an id that appears more than once, with every line it is on.
type Dup struct {
	ID    int
	Lines []int
}

// Doc is one doc's rules.
type Doc struct {
	Rules      []Rule
	Unnumbered []Unnumbered
	Duplicates []Dup
	// Blocks holds, per id, every line from the id's line up to the next
	// id's line — the attribution rule applied. Lines before the first
	// id belong to nothing.
	Blocks map[int]string
}

// Entry is one `## #n` entry in a reasons file.
type Entry struct {
	ID   int
	Line int
	// Since is the ticket that established the rule, where known.
	Since string
	// Revisit is the condition under which the rule is worth reopening.
	Revisit string
	// Retired names the ticket that stopped the rule and why; non-empty
	// means the rule no longer has a line in the doc.
	Retired string
	// Body is the reason: the prose after the metadata lines.
	Body string
}

// IsRetired reports whether the rule this entry belongs to has been
// withdrawn.
func (e Entry) IsRetired() bool { return e.Retired != "" }

// File is one reasons file.
type File struct {
	Entries    []Entry
	Duplicates []Dup
	// Malformed are h2 headings that are not `## #n`. Each is a finding,
	// and the lines under one belong to no entry — a bad heading must
	// not silently extend the entry above it.
	Malformed []Unnumbered
	// Blocks holds each entry's whole text, heading to next h2.
	Blocks map[int]string
}

var (
	headingRe = regexp.MustCompile(`^(#{2,6})\s+(.+?)\s*$`)
	headingID = regexp.MustCompile(`^(#{2,6})\s+#(\d+)(?:\s+(.*?))?\s*$`)
	// bulletID matches on the bullet's first line only. The bold lead
	// may wrap — Catapult's systems/substrate.md carries one whose
	// closing ** is on the second line — and a pattern demanding the
	// close on line one would read that rule as unnumbered.
	bulletID = regexp.MustCompile(`^- \*\*#(\d+)\s`)
	entryRe  = regexp.MustCompile(`^##\s+#(\d+)\s*$`)
	h2Re     = regexp.MustCompile(`^##\s+(.*?)\s*$`)
	idToken  = regexp.MustCompile(`^#(\d+)(?:\s+|$)`)
)

// StripID returns a heading's text without its id token: "#3 States" is
// "States". The doc lint normalises through this, because its own
// normalisation trims `#` from both ends and `## #3 States` would
// otherwise read as "3 states" — not a banned heading — and the token
// meant to be inert would have disarmed the lint.
func StripID(text string) string {
	return strings.TrimSpace(idToken.ReplaceAllString(strings.TrimSpace(text), ""))
}

// IsReasonsFile reports whether a file name is a reasons sibling.
func IsReasonsFile(name string) bool {
	return strings.HasSuffix(name, ".reasons.md")
}

// ReasonsFileFor names the sibling of a doc file: foundation.md is
// foundation.reasons.md.
func ReasonsFileFor(docFile string) string {
	return strings.TrimSuffix(docFile, ".md") + ".reasons.md"
}

// masked returns the lines with front matter and fenced blocks blanked,
// line count preserved. The decisions index deletes fences outright,
// which is fine for an index and wrong here: an audit reports line
// numbers, and a number that drifts by the size of every fence above it
// sends the reader to the wrong rule.
func masked(lines []string) []string {
	out := make([]string, len(lines))
	copy(out, lines)
	if len(out) > 0 && strings.TrimSpace(out[0]) == "---" {
		out[0] = ""
		for i := 1; i < len(out); i++ {
			closing := strings.TrimSpace(out[i]) == "---"
			out[i] = ""
			if closing {
				break
			}
		}
	}
	fenced := false
	for i, l := range out {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
			out[i] = ""
			continue
		}
		if fenced {
			out[i] = ""
		}
	}
	return out
}

// ParseDoc reads one doc's rules.
//
// Every h2–h6 heading is a rule or an unnumbered heading. A top-level
// bullet is a rule when its bold lead opens with the token, anywhere in
// the doc; under `## Standing decisions` — the section Catapult's system
// docs keep their decisions in — a bullet without one is unnumbered.
// Bullets elsewhere are prose: a screen doc enumerates a decision's
// parts as bullets, and those are parts, not rules.
//
// The container heading needs its id too, as do "Initial vs target" and
// "Depends on": they cost a token each and they leave the attribution
// rule with no exceptions, which is the same reason the decisions index
// indexes every heading rather than deciding which ones are decisions.
func ParseDoc(content string) Doc {
	lines := strings.Split(content, "\n")
	m := masked(lines)
	d := Doc{Blocks: map[int]string{}}
	seen := map[int][]int{}
	blocks := map[int][]string{}
	cur := 0
	inStanding, standingLevel := false, 0
	for i, l := range m {
		n := i + 1
		switch {
		case headingRe.MatchString(l):
			level := len(headingRe.FindStringSubmatch(l)[1])
			text := StripID(headingRe.FindStringSubmatch(l)[2])
			if hm := headingID.FindStringSubmatch(l); hm != nil {
				id, _ := strconv.Atoi(hm[2])
				d.Rules = append(d.Rules, Rule{ID: id, Kind: KindHeading, Line: n, Text: strings.TrimSpace(hm[3]), Raw: lines[i]})
				seen[id] = append(seen[id], n)
				cur = id
			} else {
				d.Unnumbered = append(d.Unnumbered, Unnumbered{Kind: KindHeading, Line: n, Text: text})
			}
			switch {
			case strings.EqualFold(text, "standing decisions"):
				inStanding, standingLevel = true, level
			case inStanding && level <= standingLevel:
				inStanding = false
			}
		case bulletID.MatchString(l):
			id, _ := strconv.Atoi(bulletID.FindStringSubmatch(l)[1])
			d.Rules = append(d.Rules, Rule{ID: id, Kind: KindBullet, Line: n, Text: boldLead(m, i), Raw: lines[i]})
			seen[id] = append(seen[id], n)
			cur = id
		case inStanding && strings.HasPrefix(l, "- "):
			d.Unnumbered = append(d.Unnumbered, Unnumbered{Kind: KindBullet, Line: n, Text: boldLead(m, i)})
		}
		if cur != 0 {
			blocks[cur] = append(blocks[cur], strings.TrimRight(lines[i], " \t"))
		}
	}
	d.Duplicates = dups(seen)
	// Trailing blank lines are trimmed off a block, because whether an
	// id is the last in its file is not a fact about the rule: a rule
	// followed by a new one at head and by end-of-file at base would
	// otherwise read as touched.
	for id, ls := range blocks {
		d.Blocks[id] = strings.TrimRight(strings.Join(ls, "\n"), "\n")
	}
	return d
}

// boldLead reads a bullet's bold lead starting at line i, following the
// wrap to the closing ** where there is one. A bullet with no bold at
// all yields its first line's text, so an unnumbered finding can still
// quote something.
func boldLead(lines []string, i int) string {
	first := strings.TrimPrefix(lines[i], "- ")
	rest, ok := strings.CutPrefix(first, "**")
	if !ok {
		return strings.TrimSpace(first)
	}
	rest = strings.TrimSpace(idToken.ReplaceAllString(rest, ""))
	if lead, _, closed := strings.Cut(rest, "**"); closed {
		return strings.TrimSpace(lead)
	}
	parts := []string{rest}
	for j := i + 1; j < len(lines); j++ {
		l := strings.TrimSpace(lines[j])
		if l == "" || !strings.HasPrefix(lines[j], " ") {
			break
		}
		if lead, _, closed := strings.Cut(l, "**"); closed {
			parts = append(parts, lead)
			break
		}
		parts = append(parts, l)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// ParseFile reads a reasons file.
//
// `## #n` opens an entry. Metadata lines are read only before the
// entry's prose — the rule the non-asks `scope:` line already follows,
// for the same reason: a sentence beginning "retired:" partway down a
// reason must not silently retire the rule. Blank lines between the
// metadata lines do not start the prose. Anything before the first
// entry is preamble and is dropped.
func ParseFile(content string) File {
	lines := strings.Split(content, "\n")
	f := File{Blocks: map[int]string{}}
	seen := map[int][]int{}
	blocks := map[int][]string{}
	var cur *Entry
	var buf []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Body = strings.TrimSpace(strings.Join(buf, "\n"))
		f.Entries = append(f.Entries, *cur)
		cur, buf = nil, nil
	}
	inEntry := 0
	for i, l := range lines {
		n := i + 1
		if em := entryRe.FindStringSubmatch(l); em != nil {
			flush()
			id, _ := strconv.Atoi(em[1])
			cur = &Entry{ID: id, Line: n}
			seen[id] = append(seen[id], n)
			inEntry = id
		} else if hm := h2Re.FindStringSubmatch(l); hm != nil {
			flush()
			f.Malformed = append(f.Malformed, Unnumbered{Kind: KindHeading, Line: n, Text: hm[1]})
			inEntry = 0
			continue
		}
		if inEntry != 0 {
			blocks[inEntry] = append(blocks[inEntry], strings.TrimRight(l, " \t"))
		}
		if cur == nil || entryRe.MatchString(l) {
			continue
		}
		if len(buf) == 0 {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if key, val, ok := metadata(l); ok {
				switch key {
				case "since":
					cur.Since = val
				case "revisit":
					cur.Revisit = val
				case "retired":
					cur.Retired = val
				}
				continue
			}
		}
		buf = append(buf, l)
	}
	flush()
	f.Duplicates = dups(seen)
	for id, ls := range blocks {
		f.Blocks[id] = strings.TrimRight(strings.Join(ls, "\n"), "\n")
	}
	return f
}

func metadata(line string) (key, val string, ok bool) {
	t := strings.TrimSpace(line)
	k, v, found := strings.Cut(t, ":")
	if !found {
		return "", "", false
	}
	switch k = strings.ToLower(strings.TrimSpace(k)); k {
	case "since", "revisit", "retired":
		return k, strings.TrimSpace(v), true
	}
	return "", "", false
}

func dups(seen map[int][]int) []Dup {
	var out []Dup
	for id, ls := range seen {
		if len(ls) > 1 {
			out = append(out, Dup{ID: id, Lines: ls})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Index is one doc and its sibling, if any.
type Index struct {
	// Dir is "systems" or "screens"; Name the basename — the mutex
	// label's name half, and the name a citation writes.
	Dir, Name string
	// Path is repo-relative: systems/foundation.md.
	Path string
	Doc  Doc
	// File is nil when the doc has no reasons sibling.
	File *File
	// FilePath is where the sibling is or would be.
	FilePath string
}

// Ported reports whether this doc is held to the audit's checks: it
// carries at least one id, or it has a sibling. Either signal arms them,
// so a doc that gained ids without a sibling still has its ids checked
// for duplicates, and a doc that gained a sibling first is held to
// numbering everything.
func (ix Index) Ported() bool {
	return len(ix.Doc.Rules) > 0 || ix.File != nil
}

// Rule finds the rule line carrying id.
func (ix Index) Rule(id int) (Rule, bool) {
	for _, r := range ix.Doc.Rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}

// Entry finds the entry recorded for id.
func (ix Index) Entry(id int) (Entry, bool) {
	if ix.File == nil {
		return Entry{}, false
	}
	for _, e := range ix.File.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Resolves reports whether a citation of id lands on something: a rule
// line, or an entry — retired included, because keeping the entry is
// exactly what lets a citation of a withdrawn rule keep meaning
// something.
func (ix Index) Resolves(id int) bool {
	if _, ok := ix.Rule(id); ok {
		return true
	}
	_, ok := ix.Entry(id)
	return ok
}

// NextID is the id a new rule in this doc mints: the highest present,
// on either side, plus one. Never a reused number, which is why entries
// count too.
func (ix Index) NextID() int {
	max := 0
	for _, r := range ix.Doc.Rules {
		if r.ID > max {
			max = r.ID
		}
	}
	if ix.File != nil {
		for _, e := range ix.File.Entries {
			if e.ID > max {
				max = e.ID
			}
		}
	}
	return max + 1
}

// Load reads one doc and its sibling. ok is false when the doc does not
// exist; a sibling with no doc is not a doc.
func Load(root, dir, name string) (ix Index, ok bool, err error) {
	ix, err = load(root, dir, name)
	if err != nil {
		return Index{}, false, err
	}
	if _, statErr := os.Stat(filepath.Join(root, dir, name+".md")); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return Index{}, false, nil
		}
		return Index{}, false, statErr
	}
	return ix, true, nil
}

// load reads whatever of the pair exists, treating a missing doc as an
// empty one. TouchedIDs needs that shape: a doc created by the pass has
// no base side, and a reasons file can exist for a doc that does not.
func load(root, dir, name string) (Index, error) {
	ix := Index{
		Dir:      dir,
		Name:     name,
		Path:     dir + "/" + name + ".md",
		FilePath: dir + "/" + ReasonsFileFor(name+".md"),
		Doc:      Doc{Blocks: map[int]string{}},
	}
	raw, err := os.ReadFile(filepath.Join(root, dir, name+".md"))
	if err == nil {
		ix.Doc = ParseDoc(string(raw))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Index{}, err
	}
	raw, err = os.ReadFile(filepath.Join(root, dir, ReasonsFileFor(name+".md")))
	if err == nil {
		f := ParseFile(string(raw))
		ix.File = &f
	} else if !errors.Is(err, os.ErrNotExist) {
		return Index{}, err
	}
	return ix, nil
}

// LoadDir reads every doc in dir — every *.md that is not README.md and
// not a reasons sibling — with its sibling. A missing dir is no docs,
// matching the file-map loader: a project without system docs has
// nothing to index yet.
func LoadDir(root, dir string) ([]Index, error) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Index
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.EqualFold(name, "README.md") || IsReasonsFile(name) {
			continue
		}
		ix, err := load(root, dir, strings.TrimSuffix(name, ".md"))
		if err != nil {
			return nil, err
		}
		out = append(out, ix)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Touched is one rule id whose text differs between two trees, with what
// stood on the base side.
type Touched struct {
	Dir, Name string
	ID        int
	// BaseRule is the rule line at base, nil when there was none.
	BaseRule *Rule
	// BaseEntry is the entry at base, nil when there was none.
	BaseEntry *Entry
	// BaseText is the rule as it stood at base — the heading, or the
	// bullet's first paragraph: the bold lead and the sentence stating
	// the rule, up to the first blank line.
	BaseText string
	// New reports that the id existed on neither side at base.
	New bool
}

// Cite is the id as a citation writes it: foundation#17.
func (t Touched) Cite() string { return t.Name + "#" + strconv.Itoa(t.ID) }

// TouchedIDs compares the per-id blocks of every changed doc or reasons
// file under systems/ or screens/ between two trees. An id is touched
// when its block in the doc or in the sibling differs, or exists on one
// side only. A file missing on either side is an empty parse.
//
// Blocks rather than rule lines, and not the diff: the diff a design
// action extracts is per commit, so its line numbers are each commit's
// parent's and not the base's, while the prose under a rule is the part
// a pass most often rewrites — a comparison of rule lines alone would
// call a rule untouched whose whole argument had been replaced.
//
// The changed list is the scope. A doc that differs between the trees
// and is not in it is not compared, because the caller has already
// decided which files this pass authored (the first-parent walk the
// design action does for the ownership audit), and a merge's edits are
// not this pass's to answer for.
func TouchedIDs(baseRoot, headRoot string, changed []string) ([]Touched, error) {
	type key struct{ dir, name string }
	var order []key
	seen := map[key]bool{}
	for _, p := range changed {
		p = filepath.ToSlash(strings.TrimSpace(p))
		dir, file, ok := strings.Cut(p, "/")
		if !ok || (dir != "systems" && dir != "screens") || strings.Contains(file, "/") || !strings.HasSuffix(file, ".md") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSuffix(file, ".reasons.md"), ".md")
		k := key{dir, name}
		if !seen[k] {
			seen[k] = true
			order = append(order, k)
		}
	}
	var out []Touched
	for _, k := range order {
		base, err := load(baseRoot, k.dir, k.name)
		if err != nil {
			return nil, err
		}
		head, err := load(headRoot, k.dir, k.name)
		if err != nil {
			return nil, err
		}
		ids := map[int]bool{}
		for _, ix := range []Index{base, head} {
			for id := range ix.Doc.Blocks {
				ids[id] = true
			}
			if ix.File != nil {
				for id := range ix.File.Blocks {
					ids[id] = true
				}
			}
		}
		var sorted []int
		for id := range ids {
			sorted = append(sorted, id)
		}
		sort.Ints(sorted)
		for _, id := range sorted {
			if entryBlock(base, id) == entryBlock(head, id) && docBlock(base, id) == docBlock(head, id) {
				continue
			}
			t := Touched{Dir: k.dir, Name: k.name, ID: id}
			if r, ok := base.Rule(id); ok {
				t.BaseRule = &r
				t.BaseText = firstParagraph(base.Doc.Blocks[id])
			}
			if e, ok := base.Entry(id); ok {
				t.BaseEntry = &e
			}
			t.New = t.BaseRule == nil && t.BaseEntry == nil && docBlock(base, id) == "" && entryBlock(base, id) == ""
			out = append(out, t)
		}
	}
	return out, nil
}

func docBlock(ix Index, id int) string { return ix.Doc.Blocks[id] }

func firstParagraph(block string) string {
	var out []string
	for _, l := range strings.Split(block, "\n") {
		if strings.TrimSpace(l) == "" {
			break
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func entryBlock(ix Index, id int) string {
	if ix.File == nil {
		return ""
	}
	return ix.File.Blocks[id]
}
