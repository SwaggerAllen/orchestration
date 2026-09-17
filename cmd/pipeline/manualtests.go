package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/filemap"
	"github.com/SwaggerAllen/orchestration/internal/manualtest"
)

// cmdManualTests prints the manual tests a run should execute, one path
// per line, for the project's CI to loop over.
//
// Two tiers (ops-free-pipeline.md §8.3 rule 6), and which one is asked
// for is explicit rather than inferred from whether --changed-files was
// passed. An empty diff and a boundary batch are different questions with
// the same answer shape, and a flag that means "everything" when a file
// is missing is a flag that runs the whole set the first time somebody
// forgets to write it — a judge pass spends the model subscription, and
// §4 makes that subscription the ceiling on all of this.
func cmdManualTests(args []string) error {
	fs := flag.NewFlagSet("manual-tests", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	changedPath := fs.String("changed-files", "", "file with one changed path per line; selects the tests whose seams the diff touches")
	all := fs.Bool("all", false, "every manual test, for the milestone boundary batch")
	root := fs.String("root", "", "project root containing systems/, screens/ and tests/manual/ (default: the config file's directory)")
	format := fs.String("format", "paths", "paths | ids | json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Exactly one. Written the other way round first — `*all ==
	// (*changedPath == "")` — which rejects every legal invocation and
	// accepts neither illegal one, and no unit test saw it because the
	// tests exercise the selection rather than the CLI. Running it once
	// against a real tree did, which is the rule this repo already states
	// about gates.
	if *all == (*changedPath != "") {
		return fmt.Errorf("manual-tests: exactly one of --changed-files or --all")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	// The config's directory, not the process's — the same correction
	// cmdAudit records, where a default of "." loaded zero maps inside
	// the pipeline checkout and every run reported clean against nothing.
	if *root == "" {
		*root = cfg.Root
	}

	tests, err := manualtest.Load(*root)
	if err != nil {
		return err
	}

	selected := tests
	if !*all {
		changed, err := readPathList(*changedPath)
		if err != nil {
			return err
		}
		systems, err := filemap.LoadDir(filepath.Join(*root, "systems"))
		if err != nil {
			return err
		}
		screens, err := filemap.LoadDir(filepath.Join(*root, "screens"))
		if err != nil {
			return err
		}
		// The seams the diff touches, read through the docs' own maps.
		// Nothing here knows a glob: that is filemap's, and one place
		// declaring what a path belongs to is the point.
		selected = manualtest.Select(tests, filemap.OwnerLabels(systems, screens, changed))
	}

	switch *format {
	case "paths":
		for _, t := range selected {
			fmt.Println(t.Path)
		}
	case "ids":
		for _, t := range selected {
			fmt.Println(t.ID)
		}
	case "json":
		// One line of JSON, for a matrix. `include` is what
		// `strategy.matrix` takes, and an empty list is a legal matrix
		// that runs no jobs — which is the right shape for a diff
		// touching no covered seam.
		var b strings.Builder
		b.WriteString(`{"include":[`)
		for i, t := range selected {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":%q,"path":%q}`, t.ID, t.Path)
		}
		b.WriteString("]}")
		fmt.Println(b.String())
	default:
		return fmt.Errorf("manual-tests: unknown --format %q (paths, ids, json)", *format)
	}
	return nil
}
