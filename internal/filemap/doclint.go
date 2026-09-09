package filemap

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/reasons"
)

// DESIGN §9 asks CI to enforce that `screens/*.md` and `systems/*.md`
// "contain no state sections and no code inventory". Until now that was
// a prompt-level rule, which means it held exactly as long as every
// model remembered it.
//
// Both halves are enforced here in their *sectioned* form — a heading
// that opens a state list or an inventory — and neither is enforced in
// prose. That split is deliberate rather than a first cut:
//
// The state rule (DESIGN §4) is about the list living in exactly one
// place. A doc that discusses a state in an argument hasn't split the
// list; a doc with a "## States" heading under it has, and that is the
// gap where undesigned work hides. Likewise a standing decision that
// names `farewell/1` is the decision doing its job, while a "## Modules"
// section is the inventory that goes stale by the next merge.
//
// So this catches structure, not mentions. A regex hunting for
// function-shaped prose would fail every good doc in this repo and
// train people to ignore the check, which is worse than not having it.
// What it does catch is the form both rules were actually written
// against, and a doc cannot drift into the bad shape by accident.

// headingRe matches an ATX heading and captures its text.
var headingRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)

// fencedRe matches fenced code blocks, whose contents are examples and
// not this doc's own structure — a doc explaining the format may show a
// "## States" heading inside a fence without having one.
var fencedRe = regexp.MustCompile("(?s)```.*?```")

// bannedHeadings maps a normalized heading to why it may not appear.
// Matched on the whole heading text, not a substring: "Standing
// decisions about state" is prose in a heading, and flagging it would
// be the false positive that gets the whole check switched off.
//
// Deliberately short. "Structure" and "Reference" were candidates and
// were dropped: both name sections a good architecture doc might well
// have, and a banned list that has to be argued with is one that gets
// bypassed rather than obeyed.
var bannedHeadings = map[string]string{
	"state":      "state sections",
	"states":     "state sections",
	"variation":  "state sections",
	"variations": "state sections",

	"module":    "code inventory",
	"modules":   "code inventory",
	"function":  "code inventory",
	"functions": "code inventory",
	"api":       "code inventory",
	"files":     "code inventory",
}

// why explains each banned kind in the terms the rule was written in,
// because a lint that only says "not allowed" gets worked around.
var why = map[string]string{
	"state sections": "the state list lives in the stories alone (DESIGN §4) — split across a doc and a set of rendered states, the two can agree while a third thing has states neither mentions",
	"code inventory": "the code is its own inventory (DESIGN §4) — a doc that lists modules or functions goes stale by the next merge",
}

// LintDir lints every doc LoadDir would read in dir. A missing dir
// lints nothing, matching LoadDir: a project without screen docs has no
// screen rules to break yet.
func LintDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		// A reasons sibling is not a doc: its h2 headings are `## #n`
		// entries, and an entry's prose may quote a doc's banned heading
		// while explaining why it is banned.
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.EqualFold(name, "README.md") || reasons.IsReasonsFile(name) {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, LintDoc(path, string(raw))...)
	}
	sort.Strings(out)
	return out, nil
}

// LintDoc reports every rule this doc breaks. Name is used in the
// message so a caller can report several docs at once.
func LintDoc(name, content string) []string {
	body := fencedRe.ReplaceAllString(content, "")
	var out []string
	for _, m := range headingRe.FindAllStringSubmatch(body, -1) {
		text := strings.TrimSpace(m[1])
		// The id token comes off before the normalisation below, which
		// trims `#` from both ends: without this `## #3 States` reads as
		// "3 states", not a banned heading, and a token meant to be inert
		// would have disarmed the lint on exactly the docs that carry it.
		key := strings.ToLower(strings.Trim(reasons.StripID(text), " *_`:#"))
		kind, banned := bannedHeadings[key]
		if !banned {
			continue
		}
		out = append(out, fmt.Sprintf("%s: heading %q — %s are not allowed here: %s", name, text, kind, why[kind]))
	}
	return out
}
