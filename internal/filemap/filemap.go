// Package filemap reads the file maps that screen and system docs carry
// in their front matter (DESIGN §4) and audits diffs against them: a PR
// touching a mapped path must carry that doc's mutex label (DESIGN §9).
// The maps live in the docs they govern — not in config — so renaming a
// boundary is one reviewed file; this package is what makes that choice
// machine-checkable.
package filemap

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/reasons"
)

// Doc is one screen or system doc's map.
type Doc struct {
	// Name is the doc's basename without extension — the mutex label's
	// name half (systems/billing.md -> system:billing).
	Name string
	// Globs are the front-matter path patterns. ** crosses directories,
	// * does not.
	Globs []string
}

// ParseFrontMatter extracts the path list from a doc's front matter:
//
//	---
//	paths:
//	  - lib/app/billing/**
//	---
//
// The key may be "paths" (systems) or "files" (screens); both are the
// same thing. A doc without front matter maps nothing — legal for a
// stub, silent for the audit.
func ParseFrontMatter(content string) ([]string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, nil
	}
	var globs []string
	inList := false
	for i := 1; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \r")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "---":
			return globs, nil
		case trimmed == "paths:" || trimmed == "files:":
			inList = true
		case inList && strings.HasPrefix(trimmed, "- "):
			g := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if g != "" {
				globs = append(globs, g)
			}
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
			// blank lines and comments don't end the list
		default:
			inList = false
		}
	}
	return nil, fmt.Errorf("front matter never closed (missing second ---)")
}

// LoadDir reads every *.md in dir (README.md excepted — it is prose about
// the directory, not a doc; a `.reasons.md` sibling excepted too — it is
// a doc's reasons, not a doc) and returns their maps. A missing dir maps
// nothing: a project without system docs simply has no system audit yet.
//
// The sibling is skipped here rather than by each caller because four of
// them read this list as "the docs that exist": the mutex audit, the
// design pass's declared-doc check, discovered-label resolution and the
// class audit. Read as a doc, `systems/foundation.reasons.md` is a
// `Doc{Name: "foundation.reasons"}` with no map, which the audit ignores
// and the label resolver accepts — a phantom `system:foundation.reasons`
// label a pass could declare and hold.
func LoadDir(dir string) ([]Doc, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var docs []Doc
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.EqualFold(name, "README.md") || reasons.IsReasonsFile(name) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		globs, err := ParseFrontMatter(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, name), err)
		}
		docs = append(docs, Doc{Name: strings.TrimSuffix(name, ".md"), Globs: globs})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs, nil
}

// Match reports whether a glob matches a repo-relative path. ** crosses
// directory boundaries; * and ? do not.
func Match(glob, path string) bool {
	re := globRe(glob)
	return re.MatchString(path)
}

func globRe(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch {
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case glob[i] == '*':
			b.WriteString("[^/]*")
		case glob[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// Records is the loaded docs of every record kind (protocol.RecordKinds),
// in the table's order. It exists so that adding a kind is a row in that
// table rather than a new parameter threaded through every caller: the
// two-parameter (systems, screens) shape had four call sites, and a
// third directory would have had to be remembered at each of them.
type Records struct {
	Kinds []KindDocs
}

// KindDocs is one kind and the docs found in its directory.
type KindDocs struct {
	Kind protocol.RecordKind
	Docs []Doc
}

// LoadRecords reads every record directory under root. A missing
// directory is no docs, as LoadDir already has it.
func LoadRecords(root string) (Records, error) {
	var r Records
	for _, k := range protocol.RecordKinds {
		docs, err := LoadDir(filepath.Join(root, filepath.FromSlash(k.Dir)))
		if err != nil {
			return Records{}, fmt.Errorf("reading %s/: %w", k.Dir, err)
		}
		r.Kinds = append(r.Kinds, KindDocs{Kind: k, Docs: docs})
	}
	return r, nil
}

// Docs returns one kind's docs by directory, nil when there is no such
// kind. Callers that want a specific directory rather than all of them.
func (r Records) Docs(dir string) []Doc {
	for _, kd := range r.Kinds {
		if kd.Kind.Dir == dir {
			return kd.Docs
		}
	}
	return nil
}

// OwnerLabels returns the mutex labels the given paths require: for each
// changed path, the label of every record doc whose file map claims it
// (DESIGN §6, §9). Sorted and deduplicated.
//
// This is Audit's "a changed path mapped by a doc requires that doc's
// label" rule read forwards instead of backwards, and it exists because
// one caller needs the labels themselves rather than a list of
// violations: releasing a mutex label a design pass no longer declares
// must never release one the audit is about to demand back.
//
// Two implementations of one rule is how they drift, so
// TestOwnerLabelsAgreesWithAudit holds them together — for any corpus,
// the labels this returns are exactly the ones whose absence Audit
// reports.
func OwnerLabels(r Records, changed []string) []string {
	seen := map[string]bool{}
	for _, path := range changed {
		for _, kd := range r.Kinds {
			for _, doc := range kd.Docs {
				for _, g := range doc.Globs {
					if Match(g, path) {
						seen[kd.Kind.LabelPrefix+doc.Name] = true
						break
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// Audit checks a diff against the maps and the labels (DESIGN §9):
//
//   - within a kind that owns exclusively, no path may be mapped by two
//     docs — overlapping ownership is an ambiguous mutex, reported even
//     with an empty diff
//   - a changed path mapped by any record doc requires that doc's label
//
// Exclusivity is the kind's (protocol.RecordKind.Exclusive) rather than
// a rule about systems specifically: a screen and a system describe one
// path from two sides, and a grammar contract's docs map overlapping
// parts of one file set on purpose.
//
// Returned strings are violations; empty means the diff is clean.
func Audit(r Records, changed, labels []string) []string {
	var out []string
	has := map[string]bool{}
	for _, l := range labels {
		has[l] = true
	}

	// Overlap: pairwise glob-set intersection is undecidable in general,
	// so overlap is judged against the actual tree via the changed set
	// plus a static exact-duplicate check — cheap, and the changed-path
	// check below catches live overlaps on every PR that exercises them.
	for _, kd := range r.Kinds {
		if !kd.Kind.Exclusive {
			continue
		}
		seen := map[string]string{}
		for _, d := range kd.Docs {
			for _, g := range d.Globs {
				if prev, dup := seen[g]; dup && prev != d.Name {
					out = append(out, fmt.Sprintf("glob %q mapped by both %s/%s.md and %s/%s.md — overlapping ownership is an ambiguous mutex", g, kd.Kind.Dir, prev, kd.Kind.Dir, d.Name))
				}
				seen[g] = d.Name
			}
		}
	}

	for _, path := range changed {
		for _, kd := range r.Kinds {
			owners := []string{}
			for _, d := range kd.Docs {
				for _, g := range d.Globs {
					if !Match(g, path) {
						continue
					}
					owners = append(owners, d.Name)
					if !has[kd.Kind.LabelPrefix+d.Name] {
						out = append(out, fmt.Sprintf("%s is mapped by %s/%s.md but the ticket carries no %s%s label — a mutex nobody took", path, kd.Kind.Dir, d.Name, kd.Kind.LabelPrefix, d.Name))
					}
					break
				}
			}
			if kd.Kind.Exclusive && len(owners) > 1 {
				out = append(out, fmt.Sprintf("%s is mapped by two docs under %s/ (%s) — overlapping ownership is an ambiguous mutex", path, kd.Kind.Dir, strings.Join(owners, ", ")))
			}
		}
	}
	return out
}
