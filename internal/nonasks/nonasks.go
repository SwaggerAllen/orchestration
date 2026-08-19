// Package nonasks parses the confirmed non-asks document and selects the
// entries one pass needs to see (DESIGN §4).
//
// The document records what the author has decided against, so a design
// pass does not propose it back. It grows by rule rather than by
// accident: `never delete an entry, because a refusal that quietly
// disappears is one the pipeline will propose again`. That rule is
// right. What was wrong is that every pass was shown every refusal.
//
// Measured on Catapult's ORC-84: 83468 bytes, inlined whole into a
// design prompt that was 92825 bytes before it reached the ticket — 71%
// of the argv cap the run was dying on, from three files, one of which
// was 65% of the total. Most of those entries are about one screen or
// one system and say nothing to a pass working somewhere else.
//
// One file with a scope list, rather than moving each entry into the
// screen or system doc that owns it. The docs would scope entries for
// free off machinery that already exists — but a refusal touching
// several things then has to be either marked global, which puts it back
// in every prompt, or copied into each doc, which is drift with extra
// steps. A list has exactly one representation for that case.
package nonasks

import (
	"strings"
)

// Universal is the scope of an entry every pass sees.
const Universal = "universal"

// Entry is one recorded refusal.
type Entry struct {
	// Title is the `## ` heading, kept so a selected slice can be
	// rendered back as the document it came from.
	Title string
	// Scope is the mutex labels this refusal is about — `screen:cap`,
	// `system:billing` — or empty for universal.
	//
	// Empty rather than a literal "universal" member, because empty is
	// what an unmigrated entry and a deliberately global one both
	// produce, and they must behave identically. Silently narrowing an
	// entry nobody scoped is the one failure mode worth designing
	// against: an unseen refusal gets re-proposed, and re-proposing is
	// the whole thing this document exists to prevent.
	Scope []string
	// Body is the prose under the heading, minus the scope line.
	Body string
}

// Universal reports whether every pass sees this entry.
func (e Entry) IsUniversal() bool { return len(e.Scope) == 0 }

// Parse reads the document into entries.
//
// A `## ` heading opens an entry; a `scope:` line anywhere before the
// entry's prose sets its scope. Anything before the first heading — a
// title, an explanation of the file — is preamble and is dropped from
// the parse, because it is not a refusal and repeating it in every
// prompt is what this package exists to stop.
//
// A file with no `## ` headings parses as a single universal entry
// holding the whole body. That is not a fallback, it is the migration
// path: every project's file works exactly as it does today until
// somebody rewrites it, and no project has to be converted before this
// ships.
//
// A file holding nothing but its own `# ` title parses as no entries at
// all, which is the shape a project starts with — the file is created in
// the bootstrap commit, before anything has been refused. "The author
// has recorded none" has to stay distinguishable from "the file was not
// there" and from "none of them are scoped to you", because those three
// license different confidence in a proposal and only one of them is
// permission.
func Parse(body string) []Entry {
	lines := strings.Split(body, "\n")
	var entries []Entry
	var cur *Entry
	var buf []string

	flush := func() {
		if cur == nil {
			return
		}
		cur.Body = strings.TrimSpace(strings.Join(buf, "\n"))
		entries = append(entries, *cur)
		cur, buf = nil, nil
	}

	for _, line := range lines {
		if title, ok := heading(line); ok {
			flush()
			cur = &Entry{Title: title}
			continue
		}
		if cur == nil {
			continue // preamble
		}
		if scope, ok := scopeLine(line); ok && len(buf) == 0 {
			cur.Scope = scope
			continue
		}
		buf = append(buf, line)
	}
	flush()

	if len(entries) == 0 {
		if rest := withoutTitle(body); rest != "" {
			return []Entry{{Body: rest}}
		}
	}
	return entries
}

// withoutTitle drops the document's own `# ` heading and any blank lines
// around it, so a file holding nothing but a title parses as no entries
// rather than as one refusal whose text is "# Confirmed non-asks".
//
// That is the shape a project starts with — the file is created in the
// bootstrap commit before anything has been refused — and it has to read
// as "the author has recorded none", which is a different fact from "the
// file was not there" and from "none of them are scoped to you".
func withoutTitle(body string) string {
	var kept []string
	started := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if !started {
			// Blank lines before the title are skipped too, or a file
			// beginning with one keeps its title: the first blank would
			// count as content and the heading after it would look like
			// prose.
			if t == "" {
				continue
			}
			started = true
			if strings.HasPrefix(t, "# ") {
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// heading matches a `## ` entry heading. Deeper levels are prose inside
// an entry, and `# ` is the document's own title.
func heading(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "## ") || strings.HasPrefix(t, "### ") {
		return "", false
	}
	return strings.TrimSpace(t[3:]), true
}

// scopeLine reads `scope: screen:cap, system:billing`.
//
// Only before the entry's prose, so a sentence beginning "scope:" partway
// down cannot silently rescope a refusal. `universal` is accepted and
// means the same as omitting the line — spelling it out is allowed
// because "this is deliberately global" and "nobody has scoped this yet"
// read very differently to whoever is maintaining the file, even though
// they select identically.
func scopeLine(line string) ([]string, bool) {
	t := strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(t, "scope:")
	if !ok {
		if rest, ok = strings.CutPrefix(t, "Scope:"); !ok {
			return nil, false
		}
	}
	var out []string
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		if part == "" || strings.EqualFold(part, Universal) {
			continue
		}
		out = append(out, part)
	}
	return out, true
}

// Select returns the entries a pass on this ticket must read.
//
// An entry is included when it is universal, when the ticket carries one
// of its scope labels, or when one of those labels' bare names appears in
// the ticket's text.
//
// That third clause is load-bearing rather than a convenience. A first
// design pass carries no mutex labels at all, because the design pass is
// what creates them (DESIGN §6) — so label matching alone would show
// design nothing but the universal set, and design is the pass the
// document is written for. Matching a name in the ticket's own words is
// the same technique the class audit already uses to ask whether a
// component was announced, and it is used here for the same reason: at
// the moment the question is asked, the words are all there is.
//
// It over-selects rather than under-selects, deliberately. A refusal
// shown to a pass that did not need it costs bytes; a refusal hidden
// from the pass that did gets re-proposed, and the author has to catch
// it at Design review having already been told it was checked.
func Select(entries []Entry, labels []string, ticketText string) []Entry {
	text := strings.ToLower(ticketText)
	held := map[string]bool{}
	for _, l := range labels {
		held[strings.ToLower(l)] = true
	}
	var out []Entry
	for _, e := range entries {
		if e.IsUniversal() || matches(e.Scope, held, text) {
			out = append(out, e)
		}
	}
	return out
}

func matches(scope []string, held map[string]bool, text string) bool {
	for _, s := range scope {
		if held[strings.ToLower(s)] {
			return true
		}
		// The bare name: `screen:cap` matches a ticket that says "cap".
		// Substring rather than word matching, because a ticket naming
		// `cap_reached` is talking about the cap screen and a rule that
		// missed it would be a rule nobody trusted.
		name := s
		if _, after, found := strings.Cut(s, ":"); found {
			name = after
		}
		if name != "" && strings.Contains(text, strings.ToLower(name)) {
			return true
		}
	}
	return false
}

// Render writes entries back as the document they came from, so a prompt
// can inline a slice without a second format to read.
func Render(entries []Entry) string {
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n")
		}
		if e.Title != "" {
			b.WriteString("## " + e.Title + "\n")
			if len(e.Scope) > 0 {
				b.WriteString("scope: " + strings.Join(e.Scope, ", ") + "\n")
			}
			b.WriteString("\n")
		}
		b.WriteString(e.Body + "\n")
	}
	return b.String()
}
