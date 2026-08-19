package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/agent"
	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/nonasks"
)

// cmdNonAsks answers "what has the author already refused about this?"
// for a scope the run has just discovered.
//
// The prompt's non-asks section is selected once, when the prompt is
// assembled, against the ticket's labels and its words (DESIGN §4). For
// dev and reconcile that is accurate — the mutex labels exist by then.
// For the pass the document is written for it is not: a first design
// pass carries no labels at all, because the design pass is what creates
// them, and its real scope is not known until it declares `screens` and
// `systems` in its outcome. The harness learns the scope one step after
// the selection needed it.
//
// So the run needs a way to ask again, mid-pass, once it knows. Telling
// it to go read the file is not that: §4's whole argument for inlining
// the document is that a prompt whose most important input is "go read
// this file" is a prompt whose most important input is optional, and an
// escape hatch phrased that way reintroduces exactly what the inlining
// was written against.
//
// A command instead, because a specific thing to run is an instruction
// and an invitation to read a file is not. It also gives the answer the
// same shape the prompt gave, which matters more than it sounds:
// selection runs through nonasks.Select here too, so the command and
// the prompt cannot disagree about what binds a scope. Two
// implementations of one rule is how the rule ends up meaning two
// things.
//
// Reads one local file and talks to nothing. No tracker, no host, no
// credential — safe to run at any point in a pass, and it cannot fail
// for a reason that has nothing to do with the question.
func cmdNonAsks(args []string) error {
	fs := flag.NewFlagSet("non-asks", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	forScope := fs.String("for", "", "comma-separated scope: screen and system labels, or their bare names")
	all := fs.Bool("all", false, "print every entry, whatever its scope")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *forScope == "" && !*all {
		return fmt.Errorf("non-asks: --for <scope> or --all (a query with neither is the whole file, which the prompt already gave you a slice of)")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	n := agent.ClaimNonAsks(cfg)
	switch {
	case n.Path == "":
		return fmt.Errorf("non-asks: this project's config names no nonAsksPath")
	case n.Err != "":
		// Loudly, and non-zero. The caller is deciding whether it is
		// about to propose something already refused, and "the file
		// could not be read" must not look like "nothing was found".
		return fmt.Errorf("non-asks: %s could not be read: %s (this is not the same as the project recording none)", n.Path, n.Err)
	case !n.Found:
		fmt.Printf("%s does not exist: this project records no refusals. Checked, not skipped.\n", n.Path)
		return nil
	}

	entries := nonasks.Parse(n.Body)
	if len(entries) == 0 {
		fmt.Printf("%s exists and records no refusals yet. Nothing has been ruled out; checked, not skipped.\n", n.Path)
		return nil
	}

	shown := entries
	if !*all {
		terms := splitScope(*forScope)
		// The terms go in as labels *and* as text, which is what makes
		// `--for roster` and `--for screen:roster` both work: Select
		// matches a label exactly, and a scope's bare name anywhere in
		// the text. A pass that has just realised it is touching the
		// roster is thinking "roster", not "screen:roster", and a query
		// tool that insisted on the second spelling would be one the
		// run gets wrong at the moment it matters.
		shown = nonasks.Select(entries, terms, strings.Join(terms, " "))
	}

	if len(shown) == 0 {
		fmt.Printf("No entry in %s is scoped to %s, and the project records no universal ones.\n", n.Path, *forScope)
		return nil
	}
	if *all {
		fmt.Printf("All %d entries in %s.\n\n", len(shown), n.Path)
	} else {
		fmt.Printf("%d of %d entries in %s bind %s — the universal ones and those scoped to it.\n\n",
			len(shown), len(entries), n.Path, *forScope)
	}
	fmt.Print(nonasks.Render(shown))
	// Said every time, not only when something matched. The question
	// this answers is "may I propose X", and the answer "nothing here
	// forbids it" is not the same as "the author has agreed to it".
	fmt.Fprintln(os.Stderr, "\nProposing against a recorded refusal is allowed; doing it silently is not. Say so in your summary.")
	return nil
}

// splitScope reads the comma-separated --for value.
func splitScope(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
