package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/reasons"
)

// cmdReasons answers "why does this rule hold?" for a rule a pass is
// about to change (DESIGN §4).
//
// The design prompt's index names the rules and their ids; the reasons
// live in each doc's `.reasons.md` sibling, which no prompt inlines —
// that is the whole saving. So the pass that is about to change a rule
// needs a way to read its reason, and the non-asks command's argument
// applies unchanged: a specific thing to run is an instruction, and an
// invitation to open a file is optional. Resolution runs through
// reasons.Resolve here as in the audit's sweep, so the command and the
// gate cannot disagree about what a citation names.
//
// Reads the checkout and talks to nothing. No tracker, no host, no
// credential — safe at any point in a pass.
func cmdReasons(args []string) error {
	fs := flag.NewFlagSet("reasons", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	root := fs.String("root", "", "project root containing systems/ and screens/ (default: the config file's directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("reasons: name one or more rules, e.g. foundation#17 or system:foundation#17")
	}
	type query struct {
		prefix, name string
		id           int
	}
	var queries []query
	for _, a := range fs.Args() {
		prefix, name, id, ok := reasons.ParseCite(a)
		if !ok {
			return fmt.Errorf("reasons: %q is not a rule citation — the form is name#n, or system:name#n / screen:name#n when a name is both", a)
		}
		queries = append(queries, query{prefix, name, id})
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	// The config file's directory, as the audit reads it: the process
	// stands wherever the harness put it, and a checkout-relative
	// default has already once made a check pass by looking where the
	// answer could not be.
	if *root == "" {
		*root = cfg.Root
	}
	var docs []reasons.Index
	for _, dir := range []string{"systems", "screens"} {
		ixs, err := reasons.LoadDir(*root, dir)
		if err != nil {
			return err
		}
		docs = append(docs, ixs...)
	}

	var unanswered []string
	for i, q := range queries {
		if i > 0 {
			fmt.Println()
		}
		cite := q.name + "#" + fmt.Sprint(q.id)
		if q.prefix != "" {
			cite = q.prefix + ":" + cite
		}
		ix, ok, why := reasons.Resolve(q.prefix, q.name, docs)
		switch {
		case why != "":
			// Ambiguity is a question not answered, not an answer, and
			// it must not exit 0 as though the pass had read a reason.
			unanswered = append(unanswered, fmt.Sprintf("%s %s", cite, why))
			continue
		case !ok:
			fmt.Printf("%s: no ported systems/%s.md or screens/%s.md in this checkout — a doc with no rule ids and no reasons file has nothing to cite. Checked, not skipped.\n", cite, q.name, q.name)
			continue
		}
		printReason(ix, q.id)
	}
	// Said every time, not only when an entry was found: the question is
	// "may I change this", and "no reason is recorded" is not "there is
	// no reason".
	fmt.Fprintln(os.Stderr, "\nA reason is a record, not a lock. Change the rule if the reason no longer holds — and amend its entry in the same commit, or the next pass reads a reason for a rule that is not there.")
	if len(unanswered) > 0 {
		return fmt.Errorf("reasons: %s", strings.Join(unanswered, "; "))
	}
	return nil
}

// printReason prints one rule as it stands and the entry behind it.
// Every absent half gets its own sentence, because "no reasons file",
// "no entry for this id" and "no rule carries this id" license
// different confidence in a change, and a pass reading only one of
// them would treat all three as permission.
func printReason(ix reasons.Index, id int) {
	rule, hasRule := ix.Rule(id)
	entry, hasEntry := ix.Entry(id)
	switch {
	case !hasRule && !hasEntry:
		fmt.Printf("%s#%d: no rule carries this id and no entry records it. The doc's highest id is #%d.\n", ix.Path, id, ix.NextID()-1)
		return
	case !hasRule && entry.IsRetired():
		fmt.Printf("%s#%d is retired (retired: %s). The rule line is gone from %s; the entry is kept as the record of why.\n\n", ix.Path, id, entry.Retired, ix.Path)
		fmt.Print(renderEntry(entry))
		return
	case !hasRule:
		fmt.Printf("%s#%d: no rule carries this id, and %s records an entry for it that is not retired — the audit reports this.\n\n", ix.Path, id, ix.FilePath)
		fmt.Print(renderEntry(entry))
		return
	}
	fmt.Printf("%s#%d\n%s\n", ix.Path, id, ruleAsItStands(ix, rule))
	switch {
	case ix.File == nil:
		fmt.Printf("\nNo %s exists: no reason is recorded for any rule in this doc. Checked, not skipped.\n", ix.FilePath)
	case !hasEntry:
		fmt.Printf("\n%s records no entry for #%d: the rule stands without a recorded reason.\n", ix.FilePath, id)
	case entry.IsRetired():
		fmt.Printf("\n%s marks #%d retired (retired: %s) while the rule line still stands — the audit reports this.\n\n", ix.FilePath, id, entry.Retired)
		fmt.Print(renderEntry(entry))
	default:
		fmt.Printf("\n%s:\n", ix.FilePath)
		fmt.Print(renderEntry(entry))
	}
}

// ruleAsItStands is the heading, or the bullet's first paragraph: the
// bold lead and the sentence that states the rule, up to the first blank
// line, which is where these docs start the argument.
func ruleAsItStands(ix reasons.Index, r reasons.Rule) string {
	if r.Kind == reasons.KindHeading {
		return r.Raw
	}
	var out []string
	for _, l := range strings.Split(ix.Doc.Blocks[r.ID], "\n") {
		if strings.TrimSpace(l) == "" {
			break
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func renderEntry(e reasons.Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## #%d\n", e.ID)
	if e.Since != "" {
		fmt.Fprintf(&b, "since: %s\n", e.Since)
	}
	if e.Revisit != "" {
		fmt.Fprintf(&b, "revisit: %s\n", e.Revisit)
	}
	if e.Retired != "" {
		fmt.Fprintf(&b, "retired: %s\n", e.Retired)
	}
	if e.Body != "" {
		b.WriteString("\n" + e.Body + "\n")
	}
	return b.String()
}
