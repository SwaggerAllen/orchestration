// Package decisions indexes the standing decisions already recorded in a
// project's screen and system docs, so a design pass is shown what its
// own docs have already settled before it settles it again.
//
// The failure it answers is Catapult's ORC-126: a design pass spent a
// full run re-verifying a fact — "no assignee/role-holder projection
// exists" — that systems/dashboard.md and screens/my-queue.md already
// stated in near-identical words, with the same evidence and the same
// conclusion. Nothing put those in front of it. The docs are in the
// checkout and the pass could have opened them, which is the same thing
// that was true of the non-asks document before it was inlined: a prompt
// whose most important input is "go read this file" is a prompt whose
// most important input is optional.
//
// # An index, not the text
//
// The non-asks document is inlined whole because it is written to be.
// These docs are not. Measured on Catapult, 2026-08-30: the
// `## Standing decisions` sections alone come to 362KB across 14 system
// docs — roughly 90k tokens, with systems/substrate.md's section at 80KB
// on its own. Inlining even one of them is not affordable, and a
// truncation that silently drops half a decision is worse than a
// pointer.
//
// So this indexes: every heading, and the bolded lead of every top-level
// bullet. The same measurement puts the whole-corpus index at 25.6KB
// (~6.4k tokens) over 369 entries, and one doc's at 0.2–4.8KB. That is a
// pointer a pass can act on — it names the decision and the file, and the
// file is one `sed` away.
//
// # Headings and bullet leads, because both are how these docs carry a
// decision
//
// Read off the real corpus rather than assumed. All 14 of Catapult's
// system docs carry a `## Standing decisions` section whose entries are
// bolded-lead bullets. None of its 6 screen docs do: a screen doc's
// headings *are* its decisions — "Cross-project, deliberately", "Empty is
// a real state", "The action-needed set is enumerated, and nothing else
// is emitted". Indexing only one of the two shapes would have covered
// half of ORC-126, which named a doc of each kind.
//
// Indexing both needs no rule about which heading is a decision, which
// is the part worth having: there is no heading name to get wrong, no
// project convention to declare, and nothing here can report a decision
// that is not in the doc. It is an input, not a gate — the worst a bad
// entry does is cost a line.
package decisions

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/nonasks"
)

// Doc is one screen or system doc's decision index.
type Doc struct {
	// Path is repo-relative, so a prompt can say where to read.
	Path string
	// Name is the basename without extension — the mutex label's name
	// half, which is what the scope selection matches on.
	Name string
	// Label is the mutex label this doc's name makes.
	Label string
	// Entries are the headings and bullet leads, in document order.
	Entries []string
}

var (
	fencedRe  = regexp.MustCompile("(?s)```.*?```")
	headingRe = regexp.MustCompile(`^(#{2,6})\s+(.+?)\s*$`)
	bulletRe  = regexp.MustCompile(`^- +(.*)$`)
	boldRe    = regexp.MustCompile(`^\*\*(.+?)\*\*`)
	sentence  = regexp.MustCompile(`(?U)^.*[.!?](\s|$)`)
)

// entryLimit truncates one entry. 200 is what the whole-corpus
// measurement above was taken at; it is a display bound rather than a
// finding about the docs, and the number that moves if the index turns
// out too long or too terse to act on.
const entryLimit = 200

// Index reads one doc's decisions.
//
// Front matter is dropped first, and not as tidiness: it is a `- `-item
// list of path globs, so a walk that did not drop it would report
// `lib/dashboard/**` as a standing decision in every doc that has one.
// The h1 is dropped too — it is the doc's name, which the caller already
// prints.
func Index(content string) []string {
	body := fencedRe.ReplaceAllString(content, "")
	lines := strings.Split(body, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				lines = lines[i+1:]
				break
			}
		}
	}
	var out []string
	var bullet string
	flush := func() {
		if bullet == "" {
			return
		}
		out = append(out, lead(bullet))
		bullet = ""
	}
	for _, l := range lines {
		switch {
		case headingRe.MatchString(l):
			flush()
			out = append(out, clip(strings.TrimSpace(headingRe.FindStringSubmatch(l)[2])))
		case bulletRe.MatchString(l):
			flush()
			bullet = bulletRe.FindStringSubmatch(l)[1]
		case bullet != "" && strings.HasPrefix(l, "  "):
			// A continuation line. Nested bullets arrive here too and are
			// folded into their parent deliberately: a sub-bullet is part
			// of the decision above it, and indexing it separately would
			// list a clause with no subject.
			// The nested bullet's own marker goes: folded in, a stray
			// "- " reads as punctuation in the middle of a sentence.
			bullet += " " + strings.TrimPrefix(strings.TrimSpace(l), "- ")
		case strings.TrimSpace(l) == "":
			// A blank line does not end a bullet: these docs wrap long
			// decisions across paragraphs inside one item.
		default:
			flush()
		}
	}
	flush()
	return out
}

// lead is a bullet's claim: the bolded opening where there is one, and
// the first sentence otherwise. The bold is what these docs put the
// decision in, and falling back to a sentence keeps a plainly-written
// bullet from indexing as nothing.
func lead(bullet string) string {
	b := strings.Join(strings.Fields(bullet), " ")
	if m := boldRe.FindStringSubmatch(b); m != nil {
		return clip(m[1])
	}
	if m := sentence.FindString(b); m != "" {
		return clip(strings.TrimSpace(m))
	}
	return clip(b)
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= entryLimit {
		return s
	}
	return strings.TrimSpace(s[:entryLimit]) + "…"
}

// LoadDir indexes every doc filemap.LoadDir would read in dir. A missing
// directory indexes nothing, matching filemap: a project that has not
// written its first system doc has no decisions to be shown.
//
// Errors reading one doc are not fatal. This is an input to a prompt, and
// failing a design run over an unreadable doc would trade a run that is
// shown less for no run at all; the caller reports what it could not read
// instead.
func LoadDir(root, dir, labelPrefix string) (docs []Doc, unreadable []string) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.EqualFold(name, "README.md") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(dir, name))
		raw, err := os.ReadFile(filepath.Join(root, dir, name))
		if err != nil {
			unreadable = append(unreadable, rel)
			continue
		}
		base := strings.TrimSuffix(name, ".md")
		docs = append(docs, Doc{
			Path:    rel,
			Name:    base,
			Label:   labelPrefix + base,
			Entries: Index(string(raw)),
		})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	return docs, unreadable
}

// Select splits the index into the docs this ticket names and the rest.
//
// The rule is nonasks.ScopeMatches, and it is that function rather than a
// copy of it: the non-asks selection answers exactly this question about
// exactly these names, including the clause that carries a first design
// pass — such a pass holds no mutex labels, because the design pass is
// what creates them (DESIGN §6), so matching the doc's bare name in the
// ticket's own words is the only thing that can select anything at all.
//
// The rest are not dropped. They are named with their entry counts, so a
// pass reaching further than the selection knows the doc exists and what
// to open — the honest form of a filter, as the non-asks section already
// puts it.
//
// It over-selects, and the substring rule is why: measured against
// Catapult, a ticket saying "dashboard" also pulls in `screens/board.md`,
// because "board" is inside "dashboard". That is the trade the non-asks
// selection already took deliberately — a doc shown to a pass that did
// not need it costs a few lines, and a doc hidden from the pass that did
// is the incident this exists to prevent. Sizes from the same run:
// 2.9KB for that ticket, 7.4KB for one naming delivery, 1.4KB for one
// naming nothing, against 26KB unscoped.
func Select(docs []Doc, labels []string, ticketText string) (shown, rest []Doc) {
	for _, d := range docs {
		if nonasks.ScopeMatches([]string{d.Label}, labels, ticketText) {
			shown = append(shown, d)
			continue
		}
		rest = append(rest, d)
	}
	return shown, rest
}
