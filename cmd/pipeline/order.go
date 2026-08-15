package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/tracker/linear"
)

// cmdOrder prints what to start next and what is waiting on what.
//
// It reads the tracker and nothing else — no host, no deploy — because
// the ordering is a question about the ticket graph, and asking for
// credentials it does not need would make a read-only report fail on a
// permission it never uses.
func cmdOrder(args []string) error {
	fs := flag.NewFlagSet("order", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	milestone := fs.String("milestone", "", "limit to one milestone by name (default: the whole project)")
	current := fs.Bool("current", false, "limit to the current milestone")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("order: LINEAR_API_KEY is not set")
	}
	ctx := context.Background()
	p := plane.New(linear.New(apiKey), cfg)
	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		return err
	}
	want := *milestone
	if *current {
		want = snap.CurrentMilestone
	}
	printOrder(os.Stdout, core.ComputeOrder(snap, want), want)
	return nil
}

func printOrder(w io.Writer, o *core.Order, milestone string) {
	scope := "the whole project"
	if milestone != "" {
		scope = "milestone " + milestone
	}
	fmt.Fprintf(w, "Ordering %s. Derived from the ticket graph as it stands now — nothing here is stored, so re-run it after anything moves.\n", scope)

	for _, l := range o.Layers {
		fmt.Fprintf(w, "\n%s — %s\n", l.Name, l.Why)
		if len(l.Tickets) == 0 {
			fmt.Fprintln(w, "  (none)")
			continue
		}
		for _, t := range l.Tickets {
			fmt.Fprintf(w, "\n  %s  %s\n", t.Key, t.Title)
			fmt.Fprintf(w, "    %s\n", t.URL)
			fmt.Fprintf(w, "    state: %s\n", t.State)
			if len(t.BlockedBy) > 0 {
				fmt.Fprintf(w, "    blocked by: %s\n", neighbours(t.BlockedBy))
			}
			if len(t.Blocks) > 0 {
				fmt.Fprintf(w, "    blocks:     %s\n", neighbours(t.Blocks))
			}
			for _, m := range t.MutexHeldBy {
				// Not a blocker, and saying so matters: design can run on
				// both at once, and only promotion collides (DESIGN §6).
				fmt.Fprintf(w, "    mutex:      %s is in flight and holds %s — designing this now is legal, but it cannot be promoted until that lands\n",
					m.Key, m.Label)
			}
		}
	}
}

func neighbours(ns []core.Neighbour) string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, fmt.Sprintf("%s (%s) %s", n.Key, n.State, n.Title))
	}
	return strings.Join(out, "\n                ")
}
