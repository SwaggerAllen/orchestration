package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/reasons"
)

// Conflict is one path a base merge left conflicted, with the file's
// content as git wrote it — markers and all.
type Conflict struct {
	Path string
	Body string
}

// Parked is a conflict the pass may not attempt, and why.
type Parked struct {
	Path string
	Why  string
}

// Triage splits a merge's conflicts into the ones a pass may resolve and
// the ones that park (DESIGN §2.4, §12).
//
// The predicate is mechanical rather than a judgement, and that is the
// point. §2.4's rule — the repo moved, both changes touch the same
// behaviour, the repo wins and the ticket stops — needs a floor that does
// not depend on the model having a careful day, and "does this hunk name
// a recorded decision" is answerable by grep where "is this conflict
// important" is not.
//
// Two ways to park, and they are different mistakes:
//
//   - a `.reasons.md` sibling, where every line is a recorded rationale;
//   - a conflicted *region* naming a rule id, anywhere in either side.
//
// Regions rather than whole files, because a systems doc carries rule ids
// throughout and parking on the file would park every conflict in every
// such doc — which is the label mutex's blast radius arriving by another
// route.
func Triage(cs []Conflict) (park []Parked, attempt []string) {
	for _, c := range cs {
		if reasons.IsReasonsFile(c.Path) {
			park = append(park, Parked{c.Path, "a reasons sibling: every line in it is a recorded rationale, and choosing a side is choosing between two of them"})
			continue
		}
		if ids := conflictedIDs(c.Body); len(ids) > 0 {
			park = append(park, Parked{c.Path, fmt.Sprintf("the conflicting hunks name %s — resolving means choosing between two recorded decisions, which is the author's (DESIGN §2.4)", strings.Join(ids, ", "))})
			continue
		}
		attempt = append(attempt, c.Path)
	}
	sort.Slice(park, func(i, j int) bool { return park[i].Path < park[j].Path })
	sort.Strings(attempt)
	return park, attempt
}

// conflictedIDs returns the rule ids named inside conflicted regions,
// deduplicated and ordered.
//
// Only between the markers: text outside them merged cleanly and is not
// what the pass would be deciding.
func conflictedIDs(body string) []string {
	seen := map[string]bool{}
	var out []string
	inside := false
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "<<<<<<<"):
			inside = true
			continue
		case strings.HasPrefix(line, ">>>>>>>"):
			inside = false
			continue
		case strings.HasPrefix(line, "======="), strings.HasPrefix(line, "|||||||"):
			// The separator and the base section's header. The base side
			// counts: a hunk is about a rule if any side names it.
			continue
		}
		if !inside || !reasons.MentionsRuleID(line) {
			continue
		}
		if t := strings.TrimSpace(line); !seen[t] {
			seen[t] = true
			out = append(out, "`"+firstRuleID(t)+"`")
		}
	}
	sort.Strings(out)
	return out
}

// firstRuleID renders the id a line names, for the park message. The
// whole line would be the wrong thing to quote: it is often a heading or
// a bullet whose prose is the thing in dispute, and the author is being
// told which rule to go and read.
func firstRuleID(line string) string {
	i := strings.Index(line, "#")
	for i >= 0 {
		rest := line[i:]
		end := strings.IndexFunc(rest[1:], func(r rune) bool {
			return !(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'Z') && r != '-'
		})
		tok := rest
		if end >= 0 {
			tok = rest[:end+1]
		}
		if len(tok) > 1 && reasons.MentionsRuleID(" "+tok) {
			return tok
		}
		next := strings.Index(line[i+1:], "#")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return "a rule id"
}

// ConflictReport is the comment a parked merge carries.
//
// It names the paths and the rules rather than stating that a conflict
// happened, because the reader cannot see the tree and is deciding
// whether to re-decide the design. A fixed string here is the defect
// `staleClaimFor` is on record for, one layer up.
func ConflictReport(park []Parked, attempt []string) string {
	var b strings.Builder
	b.WriteString("Merging `origin/main` into this branch left conflicts the pass may not resolve, so it stopped without changing anything (DESIGN §2.4).\n\n")
	for _, p := range park {
		fmt.Fprintf(&b, "- **%s** — %s\n", p.Path, p.Why)
	}
	if len(attempt) > 0 {
		b.WriteString("\nAlso conflicted, and resolvable — they will be attempted once the above is settled:\n\n")
		for _, a := range attempt {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}
	b.WriteString("\nThe base moved under this ticket. Re-deciding the design is the way out: send it to `Ready for redesign` once you have chosen.\n")
	return b.String()
}
