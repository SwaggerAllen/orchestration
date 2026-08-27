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
// Most section citations name their document by a project shorthand —
// `v5 §7.8`, `conventions §2`, `dsl-syntax.md §15.1` — 896 of them
// against 33 explicit paths on Catapult's tree. They resolve through the
// project's own `citationShorthands` table, and a project that has not
// written one gets the explicit-path coverage alone: a shorthand nobody
// declared is not checked and does not fail.
//
// The table is a whitelist and lookup is case-sensitive, both for the
// same reason — see resolve and Sweep, where each is applied.
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
var cite = regexp.MustCompile("((?:[A-Za-z0-9._-]+/)*[A-Za-z0-9._-]+)[`']{0,2}(?:s)?[ ,]{0,3}§((?:[A-Z]\\.)?[0-9]+(?:\\.[0-9]+)*)")

// heading matches a numbered markdown heading: `### 7.8 Containers...`.
// The number is what a citation resolves against; the text after it is
// free to be rewritten, which is the point of citing a rule by number
// rather than by the shape of the document.
//
// The number may carry a part letter — `### A.1.4 Reviews`. Catapult's
// vendored v4 spec numbers every section that way, and 17 citations of
// it write the letter. Without this they resolve against nothing and a
// correct citation is reported as dangling, which is the false-failure
// class this package exists to avoid rather than create.
var heading = regexp.MustCompile(`^#+[ \t]+((?:[A-Z]\.)?[0-9]+(?:\.[0-9]+)*)\.?[ \t]`)

// A Problem is one citation that does not resolve.
type Problem struct {
	Path    string // the file holding the citation
	Line    int
	Doc     string // the document as the citation writes it
	Via     string // the path a shorthand resolved to, empty for an explicit path
	Section string // the section named
	Why     string
}

func (p Problem) String() string {
	// A shorthand is named as written and then as resolved. The writer
	// searches for what they typed; the reader fixing it needs the file.
	doc := p.Doc
	if p.Via != "" {
		doc = fmt.Sprintf("%s (%s)", p.Doc, p.Via)
	}
	return fmt.Sprintf("%s:%d: cites %s §%s, which %s", p.Path, p.Line, doc, p.Section, p.Why)
}

// Sweep resolves every explicit-file section citation in paths against
// the documents they name. Paths are relative to root.
//
// A document that cannot be read is reported once per citation rather
// than skipped: a citation naming a file that no longer exists is the
// same defect as one naming a section that no longer exists, and
// skipping it silently is how the check loses the case it was built for.
// shorthands maps a project's own name for a document to its
// repo-relative path. Only entries that name a path belong in it: an
// entry recording why a shorthand cannot resolve is left out by the
// caller, which is what makes "unchecked" and "absent from the map" one
// behaviour here rather than two branches.
//
// Lookup is case-sensitive, and that is the whitelist holding. Folding
// case would resolve prose like "the design §4 said" against a DESIGN
// entry — `design` is an ordinary word in these corpora, and a
// whitelist whose keys silently widen to every capitalisation is not a
// whitelist. A project spelling one shorthand two ways lists both.
func Sweep(root string, paths []string, shorthands map[string]string) ([]Problem, error) {
	sections := map[string]map[string]bool{}
	var out []Problem
	for _, rel := range paths {
		found, err := scan(root, rel, sections, shorthands)
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

func scan(root, rel string, sections map[string]map[string]bool, shorthands map[string]string) ([]Problem, error) {
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
			written, section := m[1], m[2]
			doc, via, ok := resolve(written, shorthands)
			if !ok {
				continue
			}
			have, err := sectionsOf(root, doc, sections)
			if err != nil {
				out = append(out, Problem{rel, line, written, via, section, err.Error()})
				continue
			}
			if !have[section] {
				out = append(out, Problem{rel, line, written, via, section,
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

// resolve turns the token a citation writes into the document it names.
// It reports false for a token that names no document, which is the
// common case and must stay silent: most words before a `§` are prose
// introducing a back-reference to the same file — `and §3`, `see §2`,
// `per §7` — 285 of them across 120 ordinary English words in one
// project's tree. None is a citation, and a matcher that treated them
// as one would report the whole corpus.
//
// Two shapes name a document. A token carrying a directory is a path,
// and must end .md to be one: that requirement is the fix for a
// measured false-positive class, where anchoring on `docs/` matched the
// substring inside `seed-docs/...` and reported correct citations as
// dangling. A token without a directory names a document only if the
// project declared it, which is why a bare `dsl-syntax.md` — 190 of
// them, and no such file at the repo root — resolves through the map or
// not at all rather than being read as a path.
func resolve(written string, shorthands map[string]string) (doc, via string, ok bool) {
	if strings.Contains(written, "/") {
		if !strings.HasSuffix(written, ".md") {
			return "", "", false
		}
		return written, "", true
	}
	path, declared := shorthands[written]
	if !declared {
		return "", "", false
	}
	return path, path, true
}

// Unused reports declared shorthands that no citation in paths names.
//
// The map is a whitelist, and a whitelist that only ever grows stops
// describing the corpus it was written for. An entry outlives the last
// citation that needed it silently: nothing fails, and the next pass
// reading the map takes it as evidence that the shorthand is in use.
// Catapult's `# catapult:allow` escape already prunes itself this way —
// a tag covering no violation is reported — and the reasoning carries
// over unchanged.
//
// A declared name counts as used when it appears where a citation names
// its document, which is why this shares cite with the sweep rather
// than matching the name loosely. A shorthand mentioned in prose — the
// map's own key, quoted in a sentence about the map — is not a use, and
// a check that counted it would report nothing for exactly the entries
// worth pruning.
//
// Lookup is case-sensitive here for the reason it is in resolve: a
// project spelling one shorthand two ways declares both, and folding
// case would report neither, each excused by the other's citations.
//
// The result is a proposal and must not gate — see the caller, where
// that rule is applied and its reason recorded.
func Unused(root string, paths []string, declared []string) ([]string, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	want := map[string]bool{}
	for _, name := range declared {
		want[name] = true
	}

	seen := map[string]bool{}
	for _, rel := range paths {
		if len(seen) == len(want) {
			break // every declared name is accounted for; the rest cannot change that
		}
		if err := namesIn(root, rel, want, seen); err != nil {
			return nil, err
		}
	}

	var out []string
	for name := range want {
		if !seen[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// namesIn records which of the wanted names one file cites, reading
// the document half of every citation and ignoring the section.
// Whether the section resolves is the sweep's question and a different
// one: a shorthand whose citations all dangle is still in use, and
// pruning it would delete the entry that makes those citations
// checkable.
func namesIn(root, rel string, want, seen map[string]bool) error {
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return nil // unreadable inputs are the caller's to enumerate, as in scan
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for s.Scan() {
		for _, m := range cite.FindAllStringSubmatch(s.Text(), -1) {
			if want[m[1]] {
				seen[m[1]] = true
			}
		}
	}
	return s.Err()
}
