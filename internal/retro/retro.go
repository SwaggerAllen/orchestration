// Package retro owns the milestone retro note: the file the boundary's
// archive step writes (DESIGN §10 step 6), and the shape its readers
// depend on.
//
// It is a package rather than a few lines in the boundary agent because
// the note has two readers with nothing else in common — the boundary's
// own duplicate detection, and the rehearsal reset's revert list — and a
// format defined at the writer and re-guessed at each reader is a format
// that drifts. The writer and the parser are round-trip tested together
// here.
//
// The note exists at all because archiving destroys information: an
// archived issue drops out of the tracker's listings, so anything only
// the live tickets could answer stops having an answer the moment the
// archive step runs. The note is the durable copy, and every fact a
// later pass needs about archived work has to be in it.
package retro

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Dir is where notes live in the project repo, one per milestone.
const Dir = "docs/retros"

// Entry is one archived ticket as the note records it.
type Entry struct {
	Key   string
	Title string
	// SHAs are the commits this ticket merged, in the order its merged
	// markers were written. Usually one — the pipeline squash-merges —
	// and empty for the tickets that closed without landing anything.
	//
	// Recorded because the rehearsal reset needs them and the archive
	// step is what takes them away: reset learns what to revert from the
	// `merged` markers of the tickets it archives, and a milestone
	// boundary archives those same tickets first. Measured on
	// orchestration-dummy: ORC-23 (PR #15) survived a reset that
	// reverted its two siblings, with `retro: Rehearsal 1` sitting
	// directly above it in the log.
	SHAs []string
}

// mergedSuffix matches the trailing merge clause of an entry line. It is
// anchored to end-of-line and the shas are hex-only, so a title ending in
// its own parenthesis cannot be read as one.
var mergedSuffix = regexp.MustCompile(` \(merged ([0-9a-f]{7,40}(?: [0-9a-f]{7,40})*)\)$`)

// entryLine matches "- KEY — Title", the part of a line that is present
// whether or not anything merged.
var entryLine = regexp.MustCompile(`^- (\S+) — (.*)$`)

// Render returns the note's whole content. Entries are sorted by key so
// a note is stable regardless of the order the tracker listed the
// tickets in.
func Render(milestone string, entries []Entry) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		line := fmt.Sprintf("- %s — %s", e.Key, e.Title)
		if len(e.SHAs) > 0 {
			line += fmt.Sprintf(" (merged %s)", strings.Join(e.SHAs, " "))
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return fmt.Sprintf("# Retro — %s\n\nShipped (archived from the tracker; this note is what duplicate detection reads, and what the rehearsal reset reverts):\n\n%s\n",
		milestone, strings.Join(lines, "\n"))
}

// Parse reads entries back out of a note.
//
// Deliberately lenient about everything except the entry lines: the note
// is a document a human reads and a model reads, so prose may be added
// around the list without breaking the parse. A line that is not an
// entry is skipped rather than erroring — and an entry line written
// before this package recorded shas parses fine, with no shas, which is
// what it honestly has.
func Parse(content string) []Entry {
	var out []Entry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, " \t\r")
		m := entryLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		e := Entry{Key: m[1], Title: m[2]}
		if s := mergedSuffix.FindStringSubmatch(e.Title); s != nil {
			e.Title = strings.TrimSuffix(e.Title, s[0])
			e.SHAs = strings.Fields(s[1])
		}
		out = append(out, e)
	}
	return out
}

// Merge folds new entries into what a note already holds, keyed on the
// issue key. Later wins on conflict, because a pass that has shas for a
// ticket knows more than one that had none — the note is written before
// the tickets are archived, so a resumed pass can be the first to see a
// `merged` marker land.
//
// This is what makes a note safe to rewrite. It used to be written once
// and never touched (PutFileIfAbsent), which held for a single pass over
// a milestone and quietly failed for a second one: the second pass
// archived its tickets, found the note already there, wrote nothing, and
// took their keys and shas with it.
func Merge(existing, added []Entry) []Entry {
	at := map[string]int{}
	out := make([]Entry, 0, len(existing)+len(added))
	for _, e := range append(append([]Entry{}, existing...), added...) {
		if i, ok := at[e.Key]; ok {
			out[i] = e
			continue
		}
		at[e.Key] = len(out)
		out = append(out, e)
	}
	return out
}
