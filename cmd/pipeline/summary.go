package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// GitHub renders `$GITHUB_STEP_SUMMARY` on the run page itself, above
// the job list, without anyone expanding a step. That is the difference
// this file is about: a log is something you go and read, a summary is
// something you have already read by the time you have found the run.
//
// Which commands write one is not "all of them". The test is whether a
// human reads the output to decide something, and whether reading it
// today means expanding a job log:
//
//	order      — the whole command is a report for a person
//	preflight  — a pass/fail table, read to answer "am I safe now"
//	audit      — when CI is red, the violation is the actionable line,
//	             and it is currently on stderr inside a collapsed step
//	setup      — read once per project, at the moment things go wrong
//	sweep      — what the beat decided; the pipeline's main observable
//	scenario   — the rehearsal's verdict
//
// The agent commands deliberately write none, and that is the more
// interesting half. An agent's outcome belongs on the ticket, because
// the tracker is the record (DESIGN §9) — every state the pipeline acts
// on is read back from it. A summary saying what an agent did would be a
// second rendering of that, authoritative-looking and not authoritative,
// and the first time the two disagreed somebody would have to work out
// which one lied. The agent's run log stays what it is: a debugging
// artifact for when the ticket does not explain itself. A failed agent
// run is not the exception it looks like — `agent abort` puts the
// failure on the ticket too.
//
// The same goes for the live-suite report: its verdict lands on the
// boundary ticket, where the author is already looking.
//
// Nothing here can fail a command. A summary is a courtesy, and a
// command that exits non-zero because it could not write one would be
// trading the job it was given for the favour it was doing.
func summarize(render func(io.Writer)) {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return // not in Actions; the render cost is not even paid
	}
	// Append: a job's steps each add to the same file, and truncating
	// would silently delete an earlier step's half of the story.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	render(f)
	fmt.Fprintln(f)
}

// summaryHeading writes the one-line title every summary opens with, so
// a job with several steps reads as several sections rather than as one
// run-on document.
func summaryHeading(w io.Writer, title string) {
	fmt.Fprintf(w, "## %s\n\n", title)
}

// codeBlock renders text that was written for a terminal — aligned
// columns, leading spaces — without markdown eating the alignment. Used
// where a command's existing output is already the clearest form of the
// answer and rewriting it in markdown would only be a second thing to
// keep in step with the first.
func codeBlock(w io.Writer, s string) {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return
	}
	fmt.Fprintf(w, "```\n%s\n```\n", s)
}

// teeTo returns a writer copying into both, for commands that stream
// their output as they work and want the same text in the summary.
func teeTo(a, b io.Writer) io.Writer { return io.MultiWriter(a, b) }
