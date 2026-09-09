package reasons

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Audit holds one ported doc to the index's invariants (DESIGN §9): every
// heading and standing decision numbered, no id twice, every live entry
// backed by a rule line, every retired entry's rule line gone, and the
// reasons file shaped as entries. Each is a finding in the direction the
// other checks cannot see — an idle half, reported the way Catapult's
// `# catapult:allow` reports a tag covering no violation.
//
// An id with no entry is not a finding. Requiring one would be met with
// a placeholder sentence, which is the line the paring pass just removed.
func Audit(ix Index) []string {
	var out []string
	for _, u := range ix.Doc.Unnumbered {
		switch u.Kind {
		case KindHeading:
			out = append(out, fmt.Sprintf("%s:%d: heading %q carries no id — every h2+ heading in a ported doc is a rule with a stable id (DESIGN §4)", ix.Path, u.Line, u.Text))
		default:
			out = append(out, fmt.Sprintf("%s:%d: standing decision %q carries no id — every bullet under Standing decisions is a rule with a stable id (DESIGN §4)", ix.Path, u.Line, u.Text))
		}
	}
	for _, d := range ix.Doc.Duplicates {
		out = append(out, fmt.Sprintf("%s: id #%d appears %d times (lines %s) — ids are never reused or renumbered", ix.Path, d.ID, len(d.Lines), lines(d.Lines)))
	}
	if ix.File == nil {
		return out
	}
	for _, m := range ix.File.Malformed {
		out = append(out, fmt.Sprintf("%s:%d: heading %q is not an entry — every h2 in a reasons file is ## #n", ix.FilePath, m.Line, m.Text))
	}
	for _, d := range ix.File.Duplicates {
		out = append(out, fmt.Sprintf("%s: entry #%d appears %d times (lines %s) — one entry per rule", ix.FilePath, d.ID, len(d.Lines), lines(d.Lines)))
	}
	for _, e := range ix.File.Entries {
		_, hasRule := ix.Rule(e.ID)
		switch {
		case !e.IsRetired() && !hasRule:
			out = append(out, fmt.Sprintf("%s: entry #%d has no rule line in %s and is not retired — restore the rule or add a retired: line", ix.FilePath, e.ID, ix.Path))
		case e.IsRetired() && hasRule:
			out = append(out, fmt.Sprintf("%s: entry #%d is retired (%s) but %s still carries rule #%d — a retired rule loses its line", ix.FilePath, e.ID, e.Retired, ix.Path, e.ID))
		}
	}
	return out
}

func lines(ls []int) string {
	var s []string
	for _, l := range ls {
		s = append(s, strconv.Itoa(l))
	}
	return strings.Join(s, ", ")
}

// citeRe is the citation grammar: `foundation#17`, or `system:foundation#17`
// / `screen:board#2` when a name is both a system and a screen doc. The
// name must be word-bounded on the left and the digits on the right, so
// a hex colour (`#17ff00`) and a bare `#144` never match; and the name
// must be a doc this project has — measured on Catapult's tree, the
// `#<digits>` forms in its docs are `PR #144`, `pre-#144`, `commitment
// #2` and colour codes, and none of them puts a doc name before the `#`.
// That whitelist is the § resolver's own rule (DESIGN §4): a token
// resolves only when the project declared it, or the sweep reports the
// corpus rather than its defects.
var citeRe = regexp.MustCompile(`\b(?:(system|screen):)?([a-z0-9_-]+)#(\d+)\b`)

// ParseCite reads one citation exactly, for a command argument.
func ParseCite(s string) (prefix, name string, id int, ok bool) {
	m := regexp.MustCompile(`^(?:(system|screen):)?([a-z0-9_-]+)#(\d+)$`).FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", "", 0, false
	}
	id, _ = strconv.Atoi(m[3])
	return m[1], m[2], id, true
}

// CiteProblem is a rule citation that lands on nothing.
type CiteProblem struct {
	Path   string
	Line   int
	Prefix string
	Name   string
	ID     int
	Why    string
}

func (p CiteProblem) String() string {
	written := p.Name
	if p.Prefix != "" {
		written = p.Prefix + ":" + p.Name
	}
	return fmt.Sprintf("%s:%d: cites %s#%d, which %s", p.Path, p.Line, written, p.ID, p.Why)
}

// Resolve finds the doc a citation names among the indexes, applying the
// prefix when given. It answers in one of four ways: the doc (ok), an
// unknown name (not a citation — nil, ok false, why empty), or a reason
// it cannot be resolved (why set): the prefixed dir has no such doc, or
// the bare name is both a system and a screen.
func Resolve(prefix, name string, docs []Index) (ix Index, ok bool, why string) {
	var found []Index
	for _, d := range docs {
		if d.Name == name && d.Ported() && (prefix == "" || d.Dir == prefix+"s") {
			found = append(found, d)
		}
	}
	switch {
	case len(found) == 1:
		return found[0], true, ""
	case len(found) > 1:
		return Index{}, false, fmt.Sprintf("is ambiguous: %s is both systems/%s.md and screens/%s.md — write system:%s or screen:%s", name, name, name, name, name)
	case prefix != "":
		return Index{}, false, fmt.Sprintf("names %s:%s, and there is no ported %ss/%s.md", prefix, name, prefix, name)
	}
	return Index{}, false, ""
}

// SweepCitations resolves every rule citation in paths (repo-relative)
// against the docs. A name no ported doc carries is not a citation. The
// scanner and its buffer match the section sweep's, and an unreadable
// input is skipped for the same reason: the caller enumerated it.
func SweepCitations(root string, paths []string, docs []Index) ([]CiteProblem, error) {
	var out []CiteProblem
	for _, rel := range paths {
		f, err := os.Open(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for line := 1; s.Scan(); line++ {
			for _, m := range citeRe.FindAllStringSubmatch(s.Text(), -1) {
				prefix, name := m[1], m[2]
				id, _ := strconv.Atoi(m[3])
				ix, ok, why := Resolve(prefix, name, docs)
				switch {
				case why != "":
					out = append(out, CiteProblem{rel, line, prefix, name, id, why})
				case !ok:
					continue
				case !ix.Resolves(id):
					if ix.File == nil {
						out = append(out, CiteProblem{rel, line, prefix, name, id, fmt.Sprintf("is not a rule in %s, and the doc has no reasons file", ix.Path)})
					} else {
						out = append(out, CiteProblem{rel, line, prefix, name, id, fmt.Sprintf("is not a rule in %s or an entry in %s", ix.Path, ix.FilePath)})
					}
				}
			}
		}
		err = s.Err()
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}
