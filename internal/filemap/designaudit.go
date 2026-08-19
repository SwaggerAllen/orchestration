package filemap

import (
	"fmt"
	"sort"
	"strings"
)

// DesignAudit reports files a design pass wrote outside the paths design
// owns (DESIGN §5, §9).
//
// The inverse of ClassAudit, and the polarity is the whole difference:
// ClassAudit flags an added file that *matches* the component globs and
// is unexplained, while this flags a written file that matches *none* of
// the design-owned globs. Same matcher, same shape, opposite question —
// so it is a function beside that one rather than a call into it.
//
// The rule it enforces was one-way until now. `dev.md` bound the dev
// agent by naming `designOwnedPaths` from the project config, and
// `design.md` bound the design agent with a hardcoded list of file
// extensions that assumed every project's design output is a `.heex`
// and a `.story.exs`. So the boundary was stated twice, in two forms,
// and the weaker one did not generalise — and nothing checked either.
//
// Measured on Catapult's ORC-84: a design pass committed ~4,000 lines of
// bundle content and six Elixir modules, 762 insertions of
// implementation, with nothing in its prompt drawing the line and
// nothing in CI noticing. The design work in that pass was right; it
// simply kept going, which is what an unbounded role does.
//
// Stated as folders rather than files on purpose. A new file inside a
// design-owned folder is not drift, and a rule that enumerated files
// would fail the first design pass that legitimately produced one
// nobody predicted.
//
// An empty owned set reports rather than passing, for the same reason
// ClassAudit does: "this project declares no design-owned paths" and
// "this pass stayed inside them" must not print the same.
func DesignAudit(owned, written []string) []string {
	if len(written) == 0 {
		return nil
	}
	if len(owned) == 0 {
		return []string{"design audit: this project's config declares no designOwnedPaths, so there is no boundary to check a design pass against (DESIGN §5)"}
	}
	var strays []string
	for _, path := range written {
		if !matchesAny(owned, path) {
			strays = append(strays, path)
		}
	}
	if len(strays) == 0 {
		return nil
	}
	sort.Strings(strays)
	// One violation, not one per file. A pass that wandered into
	// implementation wrote a directory's worth, and a hundred lines of
	// identical findings buries the one sentence that says what to do.
	return []string{fmt.Sprintf(
		"a design pass wrote %d file(s) outside the paths design owns (DESIGN §5): %s\n\nDesign owns %s. Everything else is dev's, including the code that implements what this pass decided — write the decision, not the implementation. If the work genuinely belongs to design, the fix is a proposal to widen designOwnedPaths in the hand-back, never an edit: pipeline.config.json is author-only and a push touching it is rejected.",
		len(strays), strings.Join(strays, ", "), strings.Join(owned, ", "))}
}
