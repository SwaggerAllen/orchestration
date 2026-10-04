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
// not depend on the model having a careful day, and "are these two sides
// arguing about the same recorded rule" is answerable by grep where "is
// this conflict important" is not.
//
// **Same rule, not any rule**, and the distinction is the whole of it. A
// rule id carries the ticket that minted it (`ORC-247-2`), precisely so
// two design passes working one doc from the same main cannot collide —
// `internal/reasons` records ORC-246 and ORC-247 both minting
// `generation#52` as the incident that produced the scheme. So two sides
// naming *different* ids are two additions that landed in the same place,
// and keeping both is the resolution. Parking those would park the case
// ticket-scoped ids exist to make safe.
//
// Three cases, and each names a different situation:
//
//  1. the sides name a rule in common — they are editing one recorded
//     decision, and choosing between them is the author's;
//  2. neither side names a rule, but the conflict sits inside a section
//     that has one — both are rewriting that rule's prose, which is the
//     same dispute arriving without the id in the hunk. This is the case
//     a `.reasons.md` entry almost always is;
//  3. otherwise — each side introduced its own id, or no rule is in
//     play. The pass may attempt it.
func Triage(cs []Conflict) (park []Parked, attempt []string) {
	for _, c := range cs {
		if why, parked := disputed(c.Body); parked {
			park = append(park, Parked{c.Path, why})
			continue
		}
		attempt = append(attempt, c.Path)
	}
	sort.Slice(park, func(i, j int) bool { return park[i].Path < park[j].Path })
	sort.Strings(attempt)
	return park, attempt
}

// region is one conflicted span: the ids each side names, and the id of
// the section it falls inside.
type region struct {
	ours, theirs []string
	enclosing    string
}

// disputed reports whether any conflicted region is an argument about one
// recorded rule, and says which.
func disputed(body string) (string, bool) {
	for _, r := range regionsOf(body) {
		if both := intersect(r.ours, r.theirs); len(both) > 0 {
			return fmt.Sprintf("both sides change %s — resolving means choosing between two recorded decisions, which is the author's (DESIGN §2.4)", strings.Join(quoteAll(both), ", ")), true
		}
		if len(r.ours) == 0 && len(r.theirs) == 0 && r.enclosing != "" {
			return fmt.Sprintf("the conflict is inside `%s` and neither side adds a rule of its own, so both are rewriting that one's prose (DESIGN §2.4)", r.enclosing), true
		}
	}
	return "", false
}

// regionsOf parses the conflict markers git wrote.
//
// The enclosing id is the nearest heading with an id *above* the region
// and outside every region — the section both sides are editing within.
func regionsOf(body string) []region {
	var out []region
	var cur *region
	side := 0 // 1 = ours, 2 = theirs
	enclosing := ""
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "<<<<<<<"):
			cur = &region{enclosing: enclosing}
			side = 1
			continue
		case strings.HasPrefix(line, "|||||||"):
			// The base section of a diff3 merge. Not a side: it is what
			// both diverged from, so a rule named only there is not one
			// either side is asserting.
			side = 0
			continue
		case strings.HasPrefix(line, "======="):
			side = 2
			continue
		case strings.HasPrefix(line, ">>>>>>>"):
			if cur != nil {
				out = append(out, *cur)
			}
			cur, side = nil, 0
			continue
		}
		if cur == nil {
			if isHeading(line) && reasons.MentionsRuleID(line) {
				enclosing = firstRuleID(strings.TrimSpace(line))
			}
			continue
		}
		if !reasons.MentionsRuleID(line) {
			continue
		}
		id := firstRuleID(strings.TrimSpace(line))
		switch side {
		case 1:
			cur.ours = appendUnique(cur.ours, id)
		case 2:
			cur.theirs = appendUnique(cur.theirs, id)
		}
	}
	return out
}

func isHeading(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

func appendUnique(xs []string, x string) []string {
	for _, e := range xs {
		if e == x {
			return xs
		}
	}
	return append(xs, x)
}

func intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range a {
		in[x] = true
	}
	var out []string
	for _, y := range b {
		if in[y] {
			out = appendUnique(out, y)
		}
	}
	sort.Strings(out)
	return out
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "`" + x + "`"
	}
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
