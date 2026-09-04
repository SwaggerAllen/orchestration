package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/host"
)

// The tail in the prompt is not always the failure: Actions appends
// post-job cleanup after the failing step. Measured on Catapult's
// ORC-224 (run 33917468841), whose last 150 lines are checkout teardown,
// a Postgres service-container dump and orphan cleanup, with no test
// output among them — so the whole log has to reach the agent as a file
// it can search.
func TestSpillCILogsWritesTheWholeLogAndNamesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ci-logs")
	f := &CIFailure{Jobs: []host.JobLog{
		{Name: "ci", Log: "the tail", Full: "line one\nline two\nline three", Lines: 3},
		{Name: "substrate suite", Log: "tail two", Full: "only one line", Lines: 1},
	}}
	SpillCILogs(f, dir)

	if f.SpillErr != "" {
		t.Fatalf("spill reported an error: %s", f.SpillErr)
	}
	for i, want := range []string{"line one\nline two\nline three", "only one line"} {
		p := f.Jobs[i].LogPath
		if p == "" {
			t.Fatalf("job %d got no LogPath, so the prompt has nothing to point at", i)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("job %d: reading %s: %v", i, p, err)
		}
		if string(got) != want {
			t.Errorf("job %d wrote %q, want the whole log %q", i, got, want)
		}
	}
	// The tail is untouched: it is still what the prompt shows.
	if f.Jobs[0].Log != "the tail" {
		t.Errorf("the spill rewrote the tail: %q", f.Jobs[0].Log)
	}
}

// A job name is not a filename. Real ones carry spaces, slashes and
// matrix brackets — "test (1.17.3, 27)", "substrate suite" — and a
// slash would write outside the directory it was handed.
func TestSpillCILogsKeepsEveryFileInsideTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ci-logs")
	f := &CIFailure{Jobs: []host.JobLog{
		{Name: "../../etc/passwd", Full: "escaped"},
		{Name: "test (1.17.3, 27)", Full: "matrix"},
		{Name: "", Full: "nameless"},
	}}
	SpillCILogs(f, dir)

	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := range f.Jobs {
		if j.LogPath == "" {
			t.Fatalf("job %d was not written", i)
		}
		got, err := filepath.Abs(j.LogPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, abs+string(filepath.Separator)) {
			t.Errorf("job %d wrote to %s, outside %s", i, got, abs)
		}
	}
}

// No directory means the previous behaviour exactly: the tail, and no
// file. A caller that has nowhere to put logs must not be broken by the
// escape hatch existing.
func TestSpillCILogsIsANoOpWithoutADirectory(t *testing.T) {
	f := &CIFailure{Jobs: []host.JobLog{{Name: "ci", Log: "tail", Full: "whole"}}}
	SpillCILogs(f, "")
	if f.Jobs[0].LogPath != "" || f.SpillErr != "" {
		t.Errorf("wrote something without a directory: path=%q err=%q", f.Jobs[0].LogPath, f.SpillErr)
	}
	SpillCILogs(nil, "somewhere") // must not panic
}

// The whole log must never reach claim.json. It is read by the reprompt
// path and by anything else that opens the file, and a multi-megabyte
// log in it would be carried by all of them to be used by none.
func TestTheWholeLogIsNeverSerializedIntoTheClaim(t *testing.T) {
	f := &CIFailure{Jobs: []host.JobLog{
		{Name: "ci", Log: "the tail", Full: "SENTINEL-FULL-LOG-BODY", Lines: 900, LogPath: "/ws/.pipeline/ci-logs/1-ci.log"},
	}}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SENTINEL-FULL-LOG-BODY") {
		t.Errorf("the full log serialized into the claim:\n%s", raw)
	}
	// The pointer to it does travel, or the prompt cannot name the file.
	for _, want := range []string{"1-ci.log", "900", "the tail"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the claim does not carry %q — the prompt reads it from here:\n%s", want, raw)
		}
	}
}
