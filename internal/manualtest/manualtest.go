// Package manualtest is the per-PR half of the manual test gate
// (ops-free-pipeline.md §8): which of a project's manual tests a diff
// obliges, and whether the set is coherent.
//
// A manual test is a file, `tests/manual/<id>.md`, and its front matter
// names the seams it covers by the docs that own them:
//
//	---
//	covers:
//	  - system:engine
//	  - screen:board
//	---
//
// **Seams by doc name, never by path glob.** The globs already live in
// the system and screen docs' own front matter, and `filemap` already
// reads a diff through them. A test restating them would be a second
// implementation of one rule, which is how the two drift — the hazard
// `OwnerLabels` and `Audit` carry a test to hold them together for. So
// selection is two hops: `filemap.OwnerLabels` turns changed paths into
// the seams they touch, and this turns seams into the tests that claim
// them. One place declares what a path belongs to.
package manualtest

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/filemap"
	"github.com/SwaggerAllen/orchestration/internal/reasons"
)

// Dir is where a project's manual tests live. Protocol rather than
// configuration, the same call `CHANGE.md` makes and for the same
// reason: a path every project shares needs no key, and a key it does
// need has to merge here before the project can name it. The project
// still adds `tests/manual/**` to `designOwnedPaths`, which is a value
// in an array it already has.
const Dir = "tests/manual"

// Test is one manual test file.
type Test struct {
	// ID is the basename without extension, which is what the check run
	// is named after and what the boundary batch reports.
	ID string
	// Path is repo-relative, always — see Load.
	Path string
	// Covers are the seams this test exercises, as filemap spells a
	// mutex label: "system:engine", "screen:board".
	Covers []string
}

// Load reads every manual test under root's Dir. A missing directory is
// not an error — a project with no manual tests yet is legal, and the
// gate has nothing to run rather than something to complain about.
//
// It takes the project root rather than the tests directory so that
// Test.Path is repo-relative by construction. Every consumer wants that
// spelling and none wants the runner's: a check run's name has to match
// across commits for rule 8 to compare two verdicts for one test, a
// sparse-checkout pattern is repo-relative by definition, and an audit
// violation naming `/tmp/…/tests/manual/x.md` tells the reader where the
// runner put the checkout instead of which file to go and fix. Written
// the other way round first, and the audit's own output said so.
func Load(root string) ([]Test, error) {
	dir := filepath.Join(root, Dir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tests []Test
	for _, e := range entries {
		name := e.Name()
		// README and the `.reasons.md` sibling are not tests. Skipped by
		// the same predicate `filemap.LoadDir` uses, because a reasons
		// file read as a test is a test that can never pass and whose id
		// nobody recognises.
		if e.IsDir() || !strings.HasSuffix(name, ".md") ||
			strings.EqualFold(name, "README.md") || reasons.IsReasonsFile(name) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		covers, err := parseCovers(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, name), err)
		}
		tests = append(tests, Test{
			ID:     strings.TrimSuffix(name, ".md"),
			Path:   path.Join(Dir, name),
			Covers: covers,
		})
	}
	sort.Slice(tests, func(i, j int) bool { return tests[i].ID < tests[j].ID })
	return tests, nil
}

// parseCovers reads the `covers:` list out of front matter, in the same
// shape filemap's `paths:`/`files:` takes.
func parseCovers(content string) ([]string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, nil
	}
	var out []string
	inList := false
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(strings.TrimRight(lines[i], " \r"))
		switch {
		case trimmed == "---":
			return out, nil
		case trimmed == "covers:":
			inList = true
		case inList && strings.HasPrefix(trimmed, "- "):
			v := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if v != "" {
				out = append(out, v)
			}
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
		default:
			inList = false
		}
	}
	return nil, fmt.Errorf("front matter is not closed by ---")
}

// Select returns the tests a diff obliges: those claiming a seam the diff
// touches. seams is what filemap.OwnerLabels returned for the same diff.
//
// This is the per-PR tier. It is a spend control rather than a speed
// optimisation — a judge pass costs the model subscription, which §4 makes
// the ceiling on everything here — and it is why the full set runs at the
// milestone boundary instead: a test the per-PR tier never selects is
// still executed once a milestone, which is what keeps `tests/manual/**`
// from becoming the pile of unchecked claims §1 refused.
func Select(tests []Test, seams []string) []Test {
	touched := map[string]bool{}
	for _, s := range seams {
		touched[s] = true
	}
	var out []Test
	for _, t := range tests {
		for _, c := range t.Covers {
			if touched[c] {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// Problems reports manual tests that cannot be selected, against the
// project's own system and screen docs. Returned strings are violations;
// empty means the set is coherent.
//
// **This is the gate on the gate, and it is the whole reason §8.2's
// accumulation is safe.** The objection to a pile of spec files was that
// staleness is silent; the answer was that a manual test is executed, so
// it cannot rot unnoticed. That answer holds only while every test is
// reachable. A test claiming a seam no doc declares — a typo, or a system
// doc that was renamed — is never selected by any diff and never fails:
// it is back to being an unchecked claim, arriving through the one door
// the argument left open. So it is an error here rather than a silence
// there.
//
// A test claiming nothing is the same defect stated more plainly, and is
// reported for the same reason.
func Problems(tests []Test, systems, screens []filemap.Doc) []string {
	known := map[string]bool{}
	for _, d := range systems {
		known["system:"+d.Name] = true
	}
	for _, d := range screens {
		known["screen:"+d.Name] = true
	}
	var out []string
	for _, t := range tests {
		if len(t.Covers) == 0 {
			out = append(out, fmt.Sprintf("%s: covers nothing, so no diff ever selects it — name a seam or delete the file", t.Path))
			continue
		}
		for _, c := range t.Covers {
			if !known[c] {
				out = append(out, fmt.Sprintf("%s: covers %q, which no system or screen doc declares — nothing will ever select it", t.Path, c))
			}
		}
	}
	return out
}
