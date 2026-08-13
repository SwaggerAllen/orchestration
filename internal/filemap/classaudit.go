package filemap

import (
	"fmt"
	"path/filepath"
	"strings"
)

// The class audit (DESIGN §9): "a new component module or theme token
// not named in the issue fails the build ... so a proposed component
// cannot arrive unannounced inside an artifact."
//
// A new component is detected by path, not by parsing the file. Reading
// `defmodule` out of Elixir would mean a second reader for every
// language a project might be written in, and the thing being enforced
// is announcement, not naming: a file that appears under the component
// paths is a component whether or not its module line parses. Path
// detection is also the honest boundary — the pipeline knows where a
// project keeps its components because the config says so, and it knows
// nothing else about the language.
//
// "Named in the issue" is a case-insensitive search of the ticket's
// description and comments for the file's base name. That is a weak
// check by construction and is meant to be: it cannot tell an argument
// from a mention, and it is not trying to. It closes the specific hole
// §9 describes — a component arriving with nobody having said it would
// — and a design that names its new component in the hand-back, as the
// prompts require, passes it without noticing it exists.
//
// The theme-token half of §9 is NOT implemented. A token is a key in a
// project's theme configuration, no project the pipeline drives defines
// one yet, and a grammar invented against no real file would be a check
// that asserts its author's guess. It stays a prompt-level rule until
// there is a theme file to read; ClassAudit says so rather than
// reporting a clean run it did not perform.

// ClassAudit reports newly added component files the ticket never
// mentions. componentGlobs comes from the project config; empty means
// the project hasn't declared where its components live, and the audit
// reports that rather than passing silently.
func ClassAudit(componentGlobs, added []string, ticketText string) []string {
	if len(componentGlobs) == 0 {
		return nil
	}
	haystack := strings.ToLower(ticketText)
	var out []string
	for _, path := range added {
		if !matchesAny(componentGlobs, path) {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if name == "" || strings.Contains(haystack, strings.ToLower(name)) {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s is a new component and the ticket never names %q — a component the issue didn't ask for is a decision, not a port (DESIGN §2.8, §9). Name it in the design's argument, or push back",
			path, name))
	}
	return out
}

func matchesAny(globs []string, path string) bool {
	for _, g := range globs {
		if Match(g, path) {
			return true
		}
	}
	return false
}
