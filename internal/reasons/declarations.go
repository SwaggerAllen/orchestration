package reasons

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Attribute declarations: the keys a project file may carry, stated by
// the rule that admits them, in the rule (DESIGN §4).
//
// A record doc that is a contract for a file format — Catapult's
// docs/dsl/ is the grammar its bundle files are written in — used to
// admit keys in prose alone, and prose cannot be checked against the code
// that reads the file. Catapult's ORC-250 measured why no scrape can
// recover it: the contract quotes keys, values, author-chosen names and
// foreign syntax in identical backticks (`xs:appinfo` and `kind: chain`
// are one shape), states refusals in the same form as keys (`extends:`
// "is a load error"), and 16 of its loader's 59 key names mean different
// things at different sites, so a flat name matched anywhere proves a
// mention and never coverage.
//
// A schema written beside the doc moves that drift rather than closing
// it: a citation from a schema entry to `chain#14` proves #14 exists, not
// that it governs the key, and it survives #14 being rewritten to drop
// the key. So the declaration is a line inside the rule, and the rule it
// belongs to is the one the attribution rule already gives every line —
// the nearest id above. Retire the rule and its declarations go with it;
// edit a declaration and the rule is touched, so the record review is
// handed its reason.
//
//	- **#14 A generating tier's `review:` is `default` or a map.** …
//	  - key `tiers.*.review`: string | map, optional
//	  - key `tiers.*.review.prompt`: string, optional
//
// A path is dot-separated from the file's root: a name, `*` for a map's
// values under keys the file chooses, and `[]` after a segment for a
// list's items. A path opening `$name` declares a shape — JSON Schema's
// $defs — that a type names to reuse it. A type is alternatives joined
// by `|`: string, integer, number, boolean, map, list, any, a "quoted"
// value, or a $shape. A named member states `required` or `optional`,
// because a contract that defaults presence has left half of each key
// unsaid; `when <sibling> is "a" | "b"` makes a member exist only while
// a sibling holds one of those values.
//
// What `when` cannot say, measured on a scratch copy of Catapult's
// docs/dsl/chain.md declaring its loader's whole grammar: a member that
// exists while a sibling holds anything *but* a value, or is absent. Its
// supplied tiers are `generator: "supplied"` and its join tiers `draft:
// "none"`, and both gate cleanly; its generating tiers are everything
// else, most omitting `generator` and taking its default, so the keys
// only they carry (`prompt`, `review`, `reconcile`, `executor`,
// `context`, `produces`) are declared ungated. The inventory stays exact
// — every key is declared, under its rule — and the schema accepts those
// keys on the other two kinds as well.

// Declaration is one `- key` line.
type Declaration struct {
	// Path is the declared path, canonical: tiers.*.review.prompt,
	// types.*.statuses[][], $entry.status.
	Path string
	// Segs is Path split: a name, "*", "[]", or a leading "$shape".
	Segs []string
	// Types are the alternatives as written: string, map, $entry, "eventual".
	Types []string
	// Presence is "required", "optional", or "" where none is stated.
	Presence string
	// When is the sibling a `when` clause tests, "" for an ungated member;
	// WhenIs the values it tests for, unquoted.
	When   string
	WhenIs []string
	// RuleID is the nearest id above the line — the rule that admits the
	// attribute — or "" when the line sits above every id.
	RuleID string
	Line   int
	Raw    string
}

// BadDeclaration is a line that opens like a declaration and does not
// parse as one.
type BadDeclaration struct {
	Line int
	Raw  string
	Why  string
}

const segName = `[A-Za-z_][A-Za-z0-9_-]*`

var (
	// declLead is what makes a line a declaration attempt: the bullet,
	// the word, and the backtick that opens a path. The backtick is the
	// anchor because prose can say "- key point" and no prose writes a
	// code span straight after "key"; measured against Catapult's
	// systems/, screens/ and docs/dsl/, no line matches.
	declLead = regexp.MustCompile("^\\s*- key `")
	declLine = regexp.MustCompile("^\\s*- key `([^`]*)`:\\s*(.*?)\\s*$")
	segRe    = regexp.MustCompile(`^(\$` + segName + `|` + segName + `|\*)((?:\[\])*)$`)
	shapeRe  = regexp.MustCompile(`^\$` + segName + `$`)
	literal  = regexp.MustCompile(`^"[^"]*"$`)
	whenRe   = regexp.MustCompile(`^when\s+(` + segName + `)\s+is\s+(.+)$`)
)

// baseTypes are the type words a declaration may use, each one JSON
// Schema type or, for any, no constraint at all.
var baseTypes = map[string]bool{
	"string": true, "integer": true, "number": true, "boolean": true,
	"map": true, "list": true, "any": true,
}

// parseDeclaration reads one line that declLead matched. why is set when
// it does not parse, and says what to fix.
func parseDeclaration(line string) (d Declaration, why string) {
	m := declLine.FindStringSubmatch(line)
	if m == nil {
		return d, "the form is - key `path`: type[, required|optional][, when sibling is \"value\"]"
	}
	segs, why := parsePath(m[1])
	if why != "" {
		return d, why
	}
	d.Segs, d.Path = segs, render(segs)
	clauses, why := splitOutsideQuotes(m[2], ',')
	if why != "" {
		return d, why
	}
	if len(clauses) == 0 || clauses[0] == "" {
		return d, fmt.Sprintf("`%s` has no type", d.Path)
	}
	alts, why := splitOutsideQuotes(clauses[0], '|')
	if why != "" {
		return d, why
	}
	for _, a := range alts {
		if !baseTypes[a] && !literal.MatchString(a) && !shapeRe.MatchString(a) {
			return d, fmt.Sprintf("type %q is not string, integer, number, boolean, map, list, any, a \"quoted\" value or a $shape", a)
		}
		d.Types = append(d.Types, a)
	}
	for _, c := range clauses[1:] {
		switch {
		case c == "required" || c == "optional":
			if d.Presence != "" {
				return d, fmt.Sprintf("`%s` states its presence twice", d.Path)
			}
			d.Presence = c
		case whenRe.MatchString(c):
			if d.When != "" {
				return d, fmt.Sprintf("`%s` has two when clauses — one discriminator per member", d.Path)
			}
			wm := whenRe.FindStringSubmatch(c)
			vals, why := splitOutsideQuotes(wm[2], '|')
			if why != "" {
				return d, why
			}
			for _, v := range vals {
				if !literal.MatchString(v) {
					return d, fmt.Sprintf("when value %s is not \"quoted\"", v)
				}
				d.WhenIs = append(d.WhenIs, strings.Trim(v, `"`))
			}
			d.When = wm[1]
		default:
			return d, fmt.Sprintf("clause %q is not required, optional or when <sibling> is \"value\"", c)
		}
	}
	return d, ""
}

// parsePath splits a path into segments, `[]` each its own.
func parsePath(p string) ([]string, string) {
	if p == "" {
		return nil, "the path is empty"
	}
	var segs []string
	for i, part := range strings.Split(p, ".") {
		m := segRe.FindStringSubmatch(part)
		if m == nil {
			return nil, fmt.Sprintf("segment %q of `%s` is not a name, `*`, or a $shape opening the path, each optionally followed by []", part, p)
		}
		if strings.HasPrefix(m[1], "$") && i > 0 {
			return nil, fmt.Sprintf("`%s` names shape %s after its first segment — a shape opens a path or is a type", p, m[1])
		}
		segs = append(segs, m[1])
		for j := 0; j < len(m[2])/2; j++ {
			segs = append(segs, "[]")
		}
	}
	return segs, ""
}

// render is a path's canonical spelling: segments joined by dots, `[]`
// attached to the segment before it.
func render(segs []string) string {
	var b strings.Builder
	for i, s := range segs {
		if s != "[]" && i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s)
	}
	return b.String()
}

// splitOutsideQuotes splits on sep where it is not inside a "quoted"
// value, trimming each part. An unclosed quote is a reason, not a split.
func splitOutsideQuotes(s string, sep byte) ([]string, string) {
	var out []string
	inQuote, start := false, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case sep:
			if !inQuote {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if inQuote {
		return nil, fmt.Sprintf("%q opens a quote it does not close", s)
	}
	return append(out, strings.TrimSpace(s[start:])), ""
}

// IsShape reports whether a declaration declares a shape's root.
func (d Declaration) IsShape() bool {
	return len(d.Segs) == 1 && strings.HasPrefix(d.Segs[0], "$")
}

// IsNamed reports whether a declaration is a named member of a map — the
// only kind of attribute that has a presence and can be gated.
func (d Declaration) IsNamed() bool {
	last := d.Segs[len(d.Segs)-1]
	return !d.IsShape() && last != "*" && last != "[]"
}

// Parent is the declared path of the attribute holding this one: "" for
// a member of the file's root and for a shape.
func (d Declaration) Parent() string {
	if len(d.Segs) < 2 {
		return ""
	}
	return render(d.Segs[:len(d.Segs)-1])
}

// Declares reports whether the doc declares path, written canonically.
func (ix Index) Declares(path string) bool {
	for _, d := range ix.Doc.Declarations {
		if d.Path == path {
			return true
		}
	}
	return false
}

// CheckDeclarations holds a doc's declarations to the shape a schema can
// be compiled from (DESIGN §4, §9). The compiler refuses on any finding,
// so the audit and `pipeline schema` cannot disagree about what compiles.
// Findings come back in line order.
func CheckDeclarations(ix Index) []string {
	type finding struct {
		line int
		msg  string
	}
	var fs []finding
	add := func(line int, format string, args ...any) {
		fs = append(fs, finding{line, fmt.Sprintf("%s:%d: ", ix.Path, line) + fmt.Sprintf(format, args...)})
	}
	for _, b := range ix.Doc.BadDeclarations {
		add(b.Line, "%q is not a declaration: %s (DESIGN §4)", strings.TrimSpace(b.Raw), b.Why)
	}
	decls := ix.Doc.Declarations
	byPath := map[string][]Declaration{}
	for _, d := range decls {
		byPath[d.Path] = append(byPath[d.Path], d)
	}
	used := map[string]bool{}
	children := map[string][]Declaration{}
	for _, d := range decls {
		if !d.IsShape() {
			children[d.Parent()] = append(children[d.Parent()], d)
		}
	}
	for _, d := range decls {
		if ds := byPath[d.Path]; len(ds) > 1 && ds[0].Line == d.Line {
			var ls []int
			for _, x := range ds {
				ls = append(ls, x.Line)
			}
			add(d.Line, "declares `%s` %d times (lines %s) — one declaration per attribute", d.Path, len(ds), lines(ls))
		}
		if d.RuleID == "" {
			add(d.Line, "declares `%s` above the doc's first rule id — a declaration belongs to the rule it sits under, and this one sits under none", d.Path)
		}
		for _, t := range d.Types {
			if strings.HasPrefix(t, "$") {
				used[t] = true
				if _, ok := byPath[t]; !ok {
					add(d.Line, "`%s` is of shape %s, which this doc does not declare", d.Path, t)
				}
			}
		}
		switch {
		case d.IsNamed() && d.Presence == "":
			add(d.Line, "`%s` is a named member and states neither required nor optional — a contract states presence rather than defaulting it", d.Path)
		case !d.IsNamed() && d.Presence != "":
			add(d.Line, "`%s` is %s and cannot be %s — presence belongs to a named member", d.Path, what(d), d.Presence)
		}
		if p := d.Parent(); p != "" {
			pds, ok := byPath[p]
			switch {
			case !ok:
				add(d.Line, "`%s` sits in `%s`, which is not declared — an attribute's container is declared too, under the rule that admits it", d.Path, p)
			case d.Segs[len(d.Segs)-1] == "[]" && !has(pds[0].Types, "list"):
				add(d.Line, "`%s` declares list items, but `%s` is %s, not a list%s", d.Path, p, strings.Join(pds[0].Types, " | "), onShape(pds[0]))
			case d.Segs[len(d.Segs)-1] != "[]" && !has(pds[0].Types, "map"):
				add(d.Line, "`%s` declares a map member, but `%s` is %s, not a map%s", d.Path, p, strings.Join(pds[0].Types, " | "), onShape(pds[0]))
			}
		}
		if d.When != "" {
			sib := d.When
			if p := d.Parent(); p != "" {
				sib = p + "." + d.When
			}
			sds, ok := byPath[sib]
			switch {
			case !d.IsNamed():
				add(d.Line, "`%s` is %s and cannot be gated — only a named member exists conditionally", d.Path, what(d))
			case !ok || !sds[0].IsNamed():
				add(d.Line, "`%s` is gated on `%s`, which is not a declared sibling", d.Path, sib)
			case sds[0].When != "":
				add(d.Line, "`%s` is gated on `%s`, which is gated itself — a discriminator exists unconditionally", d.Path, sib)
			case !has(sds[0].Types, "string"):
				vals := literals(sds[0].Types)
				if len(vals) == 0 {
					add(d.Line, "`%s` is gated on `%s`, which is %s and holds no value a when can name", d.Path, sib, strings.Join(sds[0].Types, " | "))
					break
				}
				for _, v := range d.WhenIs {
					if !vals[v] {
						add(d.Line, "`%s` is gated on `%s` being %q, which `%s` cannot hold", d.Path, sib, v, sib)
					}
				}
			}
		}
	}
	for _, d := range decls {
		if d.IsShape() && !used[d.Segs[0]] {
			add(d.Line, "declares shape `%s` and no attribute is of it — a shape nothing uses is a declaration nobody reads", d.Path)
		}
	}
	var parents []string
	for p := range children {
		parents = append(parents, p)
	}
	sort.Strings(parents)
	for _, p := range parents {
		var star, gated *Declaration
		for i, c := range children[p] {
			if c.Segs[len(c.Segs)-1] == "*" && star == nil {
				star = &children[p][i]
			}
			if c.When != "" && gated == nil {
				gated = &children[p][i]
			}
		}
		if star != nil && gated != nil {
			name := "the file's root"
			if p != "" {
				name = "`" + p + "`"
			}
			add(gated.Line, "%s declares both `*` and a member gated by when — a map open to any key cannot close one by condition", name)
		}
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].line < fs[j].line })
	var out []string
	for _, f := range fs {
		out = append(out, f.msg)
	}
	return out
}

func what(d Declaration) string {
	switch {
	case d.IsShape():
		return "a shape"
	case d.Segs[len(d.Segs)-1] == "*":
		return "a map's values"
	default:
		return "a list's items"
	}
}

// onShape points a writer at the shape when a container is one, because
// a member declared under a $ref'd node would compile to nothing.
func onShape(d Declaration) string {
	for _, t := range d.Types {
		if strings.HasPrefix(t, "$") {
			return " — declare the member on " + t + " itself"
		}
	}
	return ""
}

func has(types []string, want string) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

func literals(types []string) map[string]bool {
	out := map[string]bool{}
	for _, t := range types {
		if literal.MatchString(t) {
			out[strings.Trim(t, `"`)] = true
		}
	}
	return out
}
