package main

import (
	"context"
	"flag"
	"fmt"
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
	ticket := fs.String("ticket", "", "ticket key to read labels from the tracker (needs LINEAR_API_KEY)")
	labelsFlag := fs.String("labels", "", "comma-separated labels (offline alternative to --ticket)")
	root := fs.String("root", ".", "project root containing systems/ and screens/")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *changedPath == "" {
		return fmt.Errorf("audit: --changed-files is required")
	}
	if (*ticket == "") == (*labelsFlag == "") {
		return fmt.Errorf("audit: exactly one of --ticket or --labels")
	}

	raw, err := os.ReadFile(*changedPath)
	if err != nil {
		return err
	}
	var changed []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			changed = append(changed, line)
		}
	}

	var labels []string
	if *labelsFlag != "" {
		for _, l := range strings.Split(*labelsFlag, ",") {
			if l = strings.TrimSpace(l); l != "" {
				labels = append(labels, l)
			}
		}
	} else {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
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
	if len(violations) == 0 {
		fmt.Printf("audit clean: %d changed paths against %d system and %d screen maps\n",
			len(changed), len(systems), len(screens))
		return nil
	}
	for _, v := range violations {
		fmt.Fprintln(os.Stderr, "  "+v)
	}
	return fmt.Errorf("audit: %d violations — a mutex nobody took is a collision nobody could prevent (DESIGN §9)", len(violations))
}
