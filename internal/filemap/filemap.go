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
// the directory, not a doc) and returns their maps. A missing dir maps
// nothing: a project without system docs simply has no system audit yet.
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
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.EqualFold(name, "README.md") {
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

// Audit checks a diff against the maps and the labels (DESIGN §9):
//
//   - no path may be mapped by two system docs — overlapping ownership is
//     an ambiguous mutex, reported even with an empty diff
//   - a changed path mapped by a system doc requires that system's label
//   - a changed path mapped by a screen doc requires that screen's label
//
// Returned strings are violations; empty means the diff is clean.
func Audit(systems, screens []Doc, changed, labels []string) []string {
	var out []string
	has := map[string]bool{}
	for _, l := range labels {
		has[l] = true
	}

	// Overlap: pairwise glob-set intersection is undecidable in general,
	// so overlap is judged against the actual tree via the changed set
	// plus a static exact-duplicate check — cheap, and the changed-path
	// check below catches live overlaps on every PR that exercises them.
	seen := map[string]string{}
	for _, d := range systems {
		for _, g := range d.Globs {
			if prev, dup := seen[g]; dup && prev != d.Name {
				out = append(out, fmt.Sprintf("glob %q mapped by both systems/%s.md and systems/%s.md — overlapping ownership is an ambiguous mutex", g, prev, d.Name))
			}
			seen[g] = d.Name
		}
	}

	for _, path := range changed {
		owners := []string{}
		for _, d := range systems {
			for _, g := range d.Globs {
				if Match(g, path) {
					owners = append(owners, d.Name)
					if !has["system:"+d.Name] {
						out = append(out, fmt.Sprintf("%s is mapped by systems/%s.md but the ticket carries no system:%s label — a mutex nobody took", path, d.Name, d.Name))
					}
					break
				}
			}
		}
		if len(owners) > 1 {
			out = append(out, fmt.Sprintf("%s is mapped by two system docs (%s) — overlapping ownership is an ambiguous mutex", path, strings.Join(owners, ", ")))
		}
		for _, d := range screens {
			for _, g := range d.Globs {
				if Match(g, path) {
					if !has["screen:"+d.Name] {
						out = append(out, fmt.Sprintf("%s is mapped by screens/%s.md but the ticket carries no screen:%s label", path, d.Name, d.Name))
					}
					break
				}
			}
		}
	}
	return out
}
