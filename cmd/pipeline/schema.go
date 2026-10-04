package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/reasons"
	"github.com/SwaggerAllen/orchestration/internal/schema"
)

// cmdSchema prints the JSON Schema one record doc's attribute
// declarations compile to (DESIGN §4), for a project's own gate to check
// its code against.
//
// That gate runs on every PR, and the pipeline audit runs only on ticket
// branches, so this refuses on every finding the audit's declaration
// check would report: an author branch that breaks a declaration fails
// here rather than compiling something the audit never saw.
//
// `--root` reads no config. The schema is a function of the docs alone,
// and a config that fails to load takes every command down — a field the
// project has declared and this binary has not is enough (CLAUDE.md, on
// adding a config field). A gate that compiles a schema should not go red
// for a config change that has nothing to do with it. Without `--root`
// the root is the config file's directory, as for every other command.
//
// Reads the checkout and talks to nothing.
func cmdSchema(args []string) error {
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config, read only for its root")
	root := fs.String("root", "", "project root holding the record directories; when set, no config is read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("schema: name one record doc, e.g. chain, or %s in front when a name is in more than one record directory", reasons.PrefixList())
	}
	prefix, name, ok := reasons.ParseDocName(fs.Arg(0))
	if !ok {
		return fmt.Errorf("schema: %q is not a doc name — the form is chain, or dsl:chain", fs.Arg(0))
	}
	if *root == "" {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		*root = cfg.Root
	}
	var docs []reasons.Index
	for _, k := range protocol.RecordKinds {
		ixs, err := reasons.LoadDir(*root, k.Dir)
		if err != nil {
			return err
		}
		docs = append(docs, ixs...)
	}
	ix, ok, why := reasons.Resolve(prefix, name, docs)
	switch {
	case why != "":
		return fmt.Errorf("schema: %s %s", fs.Arg(0), why)
	case !ok:
		return fmt.Errorf("schema: no ported %s under %s — a doc with no rule ids has no declarations to compile", candidatePaths(name), *root)
	}
	s, problems := schema.Compile(ix)
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		return fmt.Errorf("schema: %s was not compiled: %d problem(s)", ix.Path, len(problems))
	}
	out, err := schema.Marshal(s)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
}
