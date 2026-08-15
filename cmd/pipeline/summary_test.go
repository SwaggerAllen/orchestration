package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A job's steps append to one file. Truncating would delete an earlier
// step's half of the story, and the failure would be invisible: the
// summary that survives is a plausible-looking one.
func TestTheSummaryAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", path)

	summarize(func(w io.Writer) { fmt.Fprintln(w, "first step") })
	summarize(func(w io.Writer) { fmt.Fprintln(w, "second step") })

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "first step") || !strings.Contains(got, "second step") {
		t.Errorf("a step overwrote another's summary:\n%s", got)
	}
	if strings.Index(got, "first step") > strings.Index(got, "second step") {
		t.Errorf("the steps are out of order:\n%s", got)
	}
}

// Outside Actions the variable is unset, and a command must behave as
// though the feature did not exist — including not paying to render it.
func TestTheSummaryIsInertOutsideActions(t *testing.T) {
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	rendered := false
	summarize(func(io.Writer) { rendered = true })
	if rendered {
		t.Error("the summary was rendered with nowhere to put it")
	}
}

// A summary is a courtesy. A command that failed because it could not
// write one would have traded the job it was given for the favour it was
// doing — so an unwritable path is silently nothing, not a panic and not
// an error.
func TestAnUnwritableSummaryIsNotAFailure(t *testing.T) {
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "no-such-dir", "summary.md"))
	summarize(func(w io.Writer) { fmt.Fprintln(w, "unreachable") })
}

// A pipe ends a table cell early and a newline ends the row. API errors
// carry both, and the cell that eats them is the one reporting the
// failure somebody is trying to read.
func TestErrorsSurviveATableCell(t *testing.T) {
	got := mdCell("GET /repos/x/y: 403 Resource not accessible\nby integration | check the scope")
	if strings.ContainsAny(got, "\n") {
		t.Errorf("a newline survived and will end the row early: %q", got)
	}
	for i, r := range got {
		if r == '|' && (i == 0 || got[i-1] != '\\') {
			t.Errorf("an unescaped pipe at %d will split the cell: %q", i, got)
		}
	}
	for _, want := range []string{"403", "not accessible", "check the scope"} {
		if !strings.Contains(got, want) {
			t.Errorf("escaping lost %q: %q", want, got)
		}
	}
}
