package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/citations"
	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/filemap"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/tracker/linear"
)

// cmdAudit is the mutex audit CI runs (DESIGN §9): every changed path
// mapped by a screen or system doc requires that doc's label on the
// ticket, and no path may be owned twice.
func cmdAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	changedPath := fs.String("changed-files", "", "file with one changed path per line (git diff --name-only)")
	addedPath := fs.String("added-files", "", "file with one ADDED path per line (git diff --diff-filter=A --name-only); enables the class audit")
	designPath := fs.String("design-files", "", "file with one path per line, written by the design agent's commits only (git log --author=pipeline-design-agent); enables the design ownership audit")
	ticket := fs.String("ticket", "", "ticket key to read labels from the tracker (needs LINEAR_API_KEY)")
	labelsFlag := fs.String("labels", "", "comma-separated labels (offline alternative to --ticket)")
	root := fs.String("root", "", "project root containing systems/ and screens/ (default: the config file's directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *changedPath == "" {
		return fmt.Errorf("audit: --changed-files is required")
	}
	if (*ticket == "") == (*labelsFlag == "") {
		return fmt.Errorf("audit: exactly one of --ticket or --labels")
	}

	changed, err := readPathList(*changedPath)
	if err != nil {
		return err
	}

	added, err := readPathList(*addedPath)
	if err != nil {
		return err
	}

	designWrote, err := readPathList(*designPath)
	if err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	// The project root is where the config file is, not where the
	// process happens to stand. CI runs this as `go -C .pipeline run
	// ...`, which puts cwd inside the *pipeline* checkout — a tree with
	// no systems/ or screens/ at all. A default of "." therefore loaded
	// zero maps, and zero maps cannot be violated: every run reported
	// "audit clean ... against 0 system and 0 screen maps" and exited 0,
	// including runs whose diff touched a mapped path under the wrong
	// label. The check was passing because it was looking somewhere the
	// answer could not be.
	if *root == "" {
		*root = cfg.Root
	}

	var labels []string
	var ticketText string
	if *labelsFlag != "" {
		for _, l := range strings.Split(*labelsFlag, ",") {
			if l = strings.TrimSpace(l); l != "" {
				labels = append(labels, l)
			}
		}
	} else {
		apiKey := os.Getenv("LINEAR_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("audit: LINEAR_API_KEY is not set (or use --labels)")
		}
		snap, err := plane.New(linear.New(apiKey), cfg).Build(context.Background(), time.Now(), false)
		if err != nil {
			return err
		}
		found := false
		for _, t := range snap.Tickets {
			if t.Key == *ticket {
				labels, found = t.Labels, true
				// The class audit asks whether the issue named the
				// component. The hand-back is where a design says so, and
				// a hand-back is a comment, so the issue is its
				// description plus its comments — not the description
				// alone (DESIGN §9, §2.8).
				var b strings.Builder
				b.WriteString(t.Title)
				b.WriteString("\n")
				b.WriteString(t.Description)
				for _, c := range t.Comments {
					b.WriteString("\n")
					b.WriteString(c.Body)
				}
				ticketText = b.String()
			}
		}
		if !found {
			return fmt.Errorf("audit: no ticket %q in the project scope", *ticket)
		}
	}

	systems, err := filemap.LoadDir(filepath.Join(*root, "systems"))
	if err != nil {
		return err
	}
	screens, err := filemap.LoadDir(filepath.Join(*root, "screens"))
	if err != nil {
		return err
	}

	violations := filemap.Audit(systems, screens, changed, labels)

	// The doc lint reads the docs as they stand rather than the diff:
	// the rule is about what a doc contains, and a doc that has held a
	// banned section since before this ticket is still holding it.
	for _, dir := range []string{"systems", "screens"} {
		found, err := filemap.LintDir(filepath.Join(*root, dir))
		if err != nil {
			return err
		}
		violations = append(violations, found...)
	}

	// The class audit needs both halves — what arrived, and what the
	// issue said. Each missing half is reported, never assumed clean:
	// "no new components" and "I couldn't tell" must not print the same.
	switch {
	case len(cfg.ComponentPaths) == 0:
		fmt.Println("class audit: skipped — this project's config declares no componentPaths (DESIGN §9)")
	case *addedPath == "":
		fmt.Println("class audit: skipped — no --added-files given, so a new component is indistinguishable from an edited one")
	case *labelsFlag != "":
		fmt.Println("class audit: skipped — --labels carries no issue text to check a component's name against; use --ticket")
	default:
		violations = append(violations, filemap.ClassAudit(cfg.ComponentPaths, added, ticketText)...)
	}

	// The design ownership audit, the mutex audit's mirror image: that
	// one asks whether a path the diff touched is claimed by a doc whose
	// label the ticket lacks, this one whether a path the *design agent*
	// wrote is one design owns at all (DESIGN §5, §9).
	//
	// Attribution is the whole reason this needs a third list. A PR
	// carries design's commits and dev's on one branch, and dev may
	// amend design-owned files on discovery — so "what the diff touched"
	// cannot answer the question. `git log --author` splits them: the
	// two agents commit under `pipeline-design-agent` and
	// `pipeline-dev-agent`, which was true before anything read it.
	//
	// Skipped states say which half was missing, for the reason the
	// class audit's do: "the design agent wrote nothing outside its
	// paths" and "nobody told me what the design agent wrote" must not
	// print the same, or a check that never ran reads as one that
	// passed. That failure is not hypothetical here — the mutex audit
	// spent its early life reporting "clean ... against 0 system and 0
	// screen maps" from inside the wrong directory.
	switch {
	case *designPath == "":
		fmt.Println("design ownership audit: skipped — no --design-files given, so design's commits are indistinguishable from dev's (DESIGN §5)")
	case len(designWrote) == 0:
		fmt.Println("design ownership audit: no commits by the design agent in this diff")
	default:
		violations = append(violations, filemap.DesignAudit(cfg.DesignOwnedPaths, designWrote)...)
	}

	// The citation sweep, whole-tree rather than docs-scoped, and that
	// scoping is the finding rather than a detail. Catapult's own manual
	// audit swept docs/, systems/, CLAUDE.md and bundles/, reported
	// clean, and left 22 dangling citations in lib/, components/ and
	// test/ — two of them inside `Catapult.Audit.Declarations`' error
	// message strings, so the audit was telling developers to go read an
	// entry that no longer existed. A dangling citation shipping as
	// runtime output.
	//
	// It resolves a section citation that names its document by path:
	// `docs/v5-design-decisions.md §7.8` against `### 7.8` in that file.
	// That is decidable, so it can honestly fail a build, which is the
	// only part of ORC-143 that can.
	//
	// **What it does not cover, said here because a clean line must not
	// overclaim.** Two thirds of section citations name their document
	// by a project shorthand — `v5 §7.8`, `conventions §2`,
	// `dsl-syntax.md §15.1` — 564 against 132 on Catapult's tree.
	// Resolving those needs a project-declared shorthand map, which is a
	// `pipeline.config.json` key and so an author-owned edit that lands
	// before the change here reading it. Prose references carry no
	// section and are not decidable at all. So "citations: N resolved"
	// is a statement about one form, not about the docs being sound.
	cited, err := citationPaths(*root)
	if err != nil {
		return err
	}
	dangling, err := citations.Sweep(*root, cited)
	if err != nil {
		return err
	}
	for _, d := range dangling {
		violations = append(violations, d.String())
	}

	if len(violations) == 0 {
		fmt.Printf("audit clean: %d changed paths against %d system and %d screen maps; %d files swept for section citations\n",
			len(changed), len(systems), len(screens), len(cited))
		return nil
	}
	for _, v := range violations {
		fmt.Fprintln(os.Stderr, "  "+v)
	}
	// A red audit is read by whoever has to fix it, and until now the
	// only place saying *what* was violated was stderr inside a
	// collapsed step of a workflow the pipeline runs on its own. The
	// check name on the PR says "ci failed"; this says which rule.
	summarize(func(w io.Writer) {
		summaryHeading(w, fmt.Sprintf("Audit — %d violations", len(violations)))
		for _, v := range violations {
			fmt.Fprintln(w, "- "+v)
		}
		fmt.Fprintln(w, "\nA mutex nobody took is a collision nobody could prevent (DESIGN §9).")
	})
	return fmt.Errorf("audit: %d violations — a mutex nobody took is a collision nobody could prevent (DESIGN §9)", len(violations))
}

// readPathList reads a newline-separated path list, treating an empty
// filename as an empty list so an optional input needs no special case.
func readPathList(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// citationPaths lists the files the citation sweep reads: every text
// file in the tree a pass could write a citation into.
//
// Whole-tree by construction, with only build output and vendored
// dependencies skipped. Narrowing this to docs/ is the mistake ORC-143
// names — the citations that had rotted longest were the ones nobody
// thought to sweep, and two of them were error strings shipping to
// developers.
func citationPaths(root string) ([]string, error) {
	skip := map[string]bool{".git": true, "deps": true, "_build": true, "node_modules": true, "cover": true}
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skip[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".md", ".ex", ".exs", ".go", ".yaml", ".yml", ".json", ".ts", ".js", ".sh":
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}
