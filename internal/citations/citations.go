// Package citations resolves references to a project's own documents
// against the sections they name (DESIGN §4).
//
// A citation rots without anyone touching it. The sentence was true when
// written; a later pass renumbered a section or removed one, and nothing
// swept the references. No prompt rule prevents that, because nothing is
// wrong at the moment of writing — only a recurring check finds it.
//
// # The grammar is the corpus's, not one invented for it
//
// ORC-143 proposed resolving `docs/non-goals.md`'s "Heading" against the
// headings in that file. Measured across Catapult's tree, that form
// appears **once** in 262 references. What the corpus actually writes is
// a section number, and this package reads exactly that:
//
//	docs/v5-design-decisions.md §7.8   -> ### 7.8 Containers, queues...
//
// 132 references take that shape. Building the other grammar would have
// swept the tree and checked one line — a gate that looks armed and is
// not, which is the failure `docs/non-goals.md` names for theme tokens:
// a grammar invented against no real file asserts its author's guess.
//
// # What this does not cover, stated because a green run must not overclaim
//
// Two thirds of section citations name their document by a project
// shorthand — `v5 §7.8`, `conventions §2` — 564 of them against 132
// explicit. Resolving those needs the project's own `citationShorthands`
// table, declared on config.Config and read by a later change than this
// one. Until a project writes it, a shorthand citation is not checked
// and does not fail.
//
// The field is declared before any project config may carry it, and
// that order is forced rather than tidy: config.Load rejects unknown
// fields at every level, so a config naming the key against a binary
// without it fails to load and the whole sweep stops. It is the
// protocol-state rule inverted — same strictness, opposite end.
//
// Prose references — "docs/non-goals.md names for theme tokens" — carry
// no section and are not decidable at all. ORC-143's own flagship
// example is one of those: it cited an entry about theme tokens where
// "theme" appears zero times in the 968-line file. A human reading it
// found that; no resolver can. This package does not claim otherwise.
package citations

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// cite matches a reference that names both a document and a section: a
// relative path to a markdown file, optionally wrapped in backticks or
// followed by a possessive, then `§` and a dotted number.
//
// The path is matched whole rather than anchored on `docs/`, and that is
// a correction rather than generality for its own sake. Anchored on
// `docs/`, the first run against Catapult's tree reported two dangling
// citations that were both correct: the references read
// `seed-docs/catapult-default-bundle-v4.md §2.3`, and the pattern
// matched the `docs/...` substring inside them, looked for a file that
// name never referred to, and failed the build on working citations.
// Two false failures on a gate is how a suppression gets added, which is
// the failure `mix.exs`'s own ignore_advisories reasoning names.
//
// A path must contain a directory. A bare `dsl-syntax.md §15.1` is a
// shorthand for `docs/dsl-syntax.md`, not a repo-root path, and treating
// it as one reported 262 dangling citations against Catapult on the
// second run of this package — every one of them correct. Bare
// filenames are the same class as `v5 §7.8` and wait on the same
// declared map.
//
// Both corrections came from running against a real tree before
// shipping, and neither was visible from the unit tests: the first
// pattern was too narrow and failed on `seed-docs/`, the second too wide
// and failed on every bare filename. A grammar is only as good as the
// corpus it was checked against.
//
// The separator is deliberately narrow — backtick, apostrophe-s, comma,
// spaces — rather than `.*`. A permissive gap matches across sentence
// boundaries and pairs a document with a section number belonging to
// something else, which is the same false failure from the other side.
var cite = regexp.MustCompile("([A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)+\\.md)[`']{0,2}(?:s)?[ ,]{0,3}§([0-9]+(?:\\.[0-9]+)*)")

// heading matches a numbered markdown heading: `### 7.8 Containers...`.
// The number is what a citation resolves against; the text after it is
// free to be rewritten, which is the point of citing a rule by number
// rather than by the shape of the document.
var heading = regexp.MustCompile(`^#+[ \t]+([0-9]+(?:\.[0-9]+)*)\.?[ \t]`)

// A Problem is one citation that does not resolve.
type Problem struct {
	Path    string // the file holding the citation
	Line    int
	Doc     string // the document cited
	Section string // the section named
	Why     string
}

func (p Problem) String() string {
	return fmt.Sprintf("%s:%d: cites %s §%s, which %s", p.Path, p.Line, p.Doc, p.Section, p.Why)
}

// Sweep resolves every explicit-file section citation in paths against
// the documents they name. Paths are relative to root.
//
// A document that cannot be read is reported once per citation rather
// than skipped: a citation naming a file that no longer exists is the
// same defect as one naming a section that no longer exists, and
// skipping it silently is how the check loses the case it was built for.
func Sweep(root string, paths []string) ([]Problem, error) {
	sections := map[string]map[string]bool{}
	var out []Problem
	for _, rel := range paths {
		found, err := scan(root, rel, sections)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

func scan(root, rel string, sections map[string]map[string]bool) ([]Problem, error) {
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return nil, nil // unreadable inputs are the caller's to enumerate
	}
	defer f.Close()

	var out []Problem
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; s.Scan(); line++ {
		for _, m := range cite.FindAllStringSubmatch(s.Text(), -1) {
			doc, section := m[1], m[2]
			have, err := sectionsOf(root, doc, sections)
			if err != nil {
				out = append(out, Problem{rel, line, doc, section, err.Error()})
				continue
			}
			if !have[section] {
				out = append(out, Problem{rel, line, doc, section,
					"names no section in that document"})
			}
		}
	}
	return out, s.Err()
}

// sectionsOf reads a cited document's numbered headings once and caches
// them. Cached by document rather than re-read per citation because one
// document is cited hundreds of times and the check runs on every PR.
func sectionsOf(root, doc string, cache map[string]map[string]bool) (map[string]bool, error) {
	if have, ok := cache[doc]; ok {
		if have == nil {
			return nil, fmt.Errorf("is a document this repository does not have")
		}
		return have, nil
	}
	f, err := os.Open(filepath.Join(root, doc))
	if err != nil {
		cache[doc] = nil
		return nil, fmt.Errorf("is a document this repository does not have")
	}
	defer f.Close()

	have := map[string]bool{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for s.Scan() {
		if m := heading.FindStringSubmatch(s.Text()); m != nil {
			// Both the heading's own number and every prefix of it: a
			// citation of §7 is satisfied by §7.8 existing under a `## 7.`
			// parent, and numbering styles differ between documents.
			n := m[1]
			have[n] = true
			for i := strings.LastIndex(n, "."); i > 0; i = strings.LastIndex(n, ".") {
				n = n[:i]
				have[n] = true
			}
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	cache[doc] = have
	return have, nil
}
