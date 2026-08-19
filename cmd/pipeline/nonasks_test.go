package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/agent"
	"github.com/SwaggerAllen/orchestration/internal/config"
)

// nonAsksProject writes a project whose config points at a non-asks file
// with a mix of scopes, and returns its config path.
func nonAsksProject(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	raw, err := json.Marshal(config.Sample())
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "pipeline.config.json")
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(root, config.DefaultNonAsksPath), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cfgPath
}

const queryDoc = `# Confirmed non-asks

## No dark mode
scope: universal

Two palettes, one designer.

## No pagination in the roster
scope: screen:roster

Thirty rows is the ceiling.

## No client-side validation
scope: screen:cap

The server is the only authority.
`

// The question this command exists for: a pass has just discovered it
// touches the roster, which its prompt's selection never named.
func TestNonAsksCommandAnswersForADiscoveredScope(t *testing.T) {
	cfg := nonAsksProject(t, queryDoc)
	out := captureStdout(t, func() {
		if err := cmdNonAsks([]string{"--config", cfg, "--for", "screen:roster"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Thirty rows") {
		t.Errorf("the roster refusal was not returned: %s", out)
	}
	if !strings.Contains(out, "Two palettes") {
		t.Errorf("the universal entry was dropped: %s", out)
	}
	if strings.Contains(out, "only authority") {
		t.Errorf("an unrelated screen's refusal came back: %s", out)
	}
}

// A pass that has just realised it is touching the roster is thinking
// "roster", not "screen:roster". A query tool that insisted on the
// second spelling is one the run gets wrong at the moment it matters.
func TestNonAsksCommandAcceptsBareNames(t *testing.T) {
	cfg := nonAsksProject(t, queryDoc)
	out := captureStdout(t, func() {
		if err := cmdNonAsks([]string{"--config", cfg, "--for", "roster"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Thirty rows") {
		t.Errorf("a bare name did not match: %s", out)
	}
}

func TestNonAsksCommandTakesAList(t *testing.T) {
	cfg := nonAsksProject(t, queryDoc)
	out := captureStdout(t, func() {
		if err := cmdNonAsks([]string{"--config", cfg, "--for", "screen:roster, screen:cap"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"Thirty rows", "only authority", "Two palettes"} {
		if !strings.Contains(out, want) {
			t.Errorf("a comma-separated scope dropped %q: %s", want, out)
		}
	}
}

// The command and the prompt must not disagree about what binds a
// scope. Two implementations of one rule is how the rule ends up
// meaning two things, so both go through nonasks.Select — asserted here
// by comparing the command's answer to the prompt section's.
func TestNonAsksCommandAgreesWithThePromptSection(t *testing.T) {
	cfg := nonAsksProject(t, queryDoc)
	out := captureStdout(t, func() {
		if err := cmdNonAsks([]string{"--config", cfg, "--for", "screen:roster"}); err != nil {
			t.Fatal(err)
		}
	})
	section := nonAsksSection(
		&agent.NonAsks{Path: "non-asks.md", Found: true, Body: queryDoc},
		"proposing", true,
		&ticketScope{Labels: []string{"screen:roster"}},
	)
	for _, title := range []string{"No dark mode", "No pagination in the roster"} {
		if strings.Contains(out, title) != strings.Contains(section, title) {
			t.Errorf("command and prompt disagree about %q", title)
		}
	}
	if strings.Contains(out, "No client-side validation") != strings.Contains(section, "No client-side validation") {
		t.Error("command and prompt disagree about an unrelated entry")
	}
}

// "The file could not be read" must never look like "nothing was
// found": the caller is deciding whether it is about to propose
// something already refused.
func TestNonAsksCommandDistinguishesMissingFromEmpty(t *testing.T) {
	absent := captureStdout(t, func() {
		if err := cmdNonAsks([]string{"--config", nonAsksProject(t, ""), "--for", "screen:cap"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(absent, "does not exist") || !strings.Contains(absent, "Checked, not skipped") {
		t.Errorf("an absent file reads as an empty answer: %s", absent)
	}
	fresh := captureStdout(t, func() {
		if err := cmdNonAsks([]string{"--config", nonAsksProject(t, "# Confirmed non-asks\n"), "--for", "screen:cap"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(fresh, "records no refusals yet") {
		t.Errorf("a file recording nothing reads as something else: %s", fresh)
	}
	nomatch := captureStdout(t, func() {
		if err := cmdNonAsks([]string{"--config", nonAsksProject(t, queryDoc), "--for", "system:nothing"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(nomatch, "Two palettes") {
		t.Errorf("a scope matching nothing still gets the universal entries: %s", nomatch)
	}
}

// A query with neither flag is the whole file, which the prompt already
// gave a slice of — refusing beats silently handing back everything and
// undoing the reason the slice exists.
func TestNonAsksCommandRefusesAnEmptyQuery(t *testing.T) {
	if err := cmdNonAsks([]string{"--config", nonAsksProject(t, queryDoc)}); err == nil {
		t.Error("a query naming no scope was answered")
	}
}
