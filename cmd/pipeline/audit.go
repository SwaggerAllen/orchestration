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

	if len(violations) == 0 {
		fmt.Printf("audit clean: %d changed paths against %d system and %d screen maps\n",
			len(changed), len(systems), len(screens))
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
