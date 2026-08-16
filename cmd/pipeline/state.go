package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/tracker/linear"
)

func cmdState(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("state: want a subcommand: ingest")
	}
	switch args[0] {
	case "ingest":
		return cmdStateIngest(args[1:])
	default:
		return fmt.Errorf("state: unknown subcommand %q", args[0])
	}
}

// cmdStateIngest adopts a project's existing tickets into the move
// record, so the §9 invariants apply to them from now rather than from
// whenever each one next happens to move.
//
// A ticket with no record is not judged — that rule is what makes the
// store safe to switch on mid-flight, because reading "I have no record"
// as "a human did this" would revert an entire backlog on the first
// sweep. The cost is that every existing ticket stays unjudged until the
// pipeline moves it, which on a slow ticket is never. This closes that
// by writing down where each ticket stands today.
//
// It adopts, it does not correct. A ticket in a state a human put it in
// illegally is adopted in that state: the record says "this is where
// things stand", and the invariants apply to what happens next. Trying
// to be cleverer would mean reverting a backlog on the strength of
// history this store was built precisely because we cannot read.
//
// Existing records are never overwritten. A ticket the pipeline has
// already moved has real provenance, and replacing it with "the control
// plane put it here" would erase the one fact the record exists to hold.
func cmdStateIngest(args []string) error {
	fs := flag.NewFlagSet("state ingest", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	dryRun := fs.Bool("dry-run", false, "print what would be adopted without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("state ingest: LINEAR_API_KEY is not set")
	}
	st := stateStore(cfg)
	if st == nil {
		return fmt.Errorf("state ingest: no state store configured — set state.url and state.project in %s and PIPELINE_STATE_TOKEN in the environment", *cfgPath)
	}
	ctx := context.Background()
	p := plane.New(linear.New(apiKey), cfg).WithState(st)
	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		return err
	}

	type adoption struct {
		key   string
		state string
	}
	var adopted []adoption
	var known int
	for _, t := range snap.Tickets {
		if _, ok := snap.Recorded[t.ID]; ok {
			known++
			continue
		}
		adopted = append(adopted, adoption{t.Key, string(t.State)})
		if *dryRun {
			continue
		}
		// From is left empty: nothing is known about where this ticket
		// came from, and inventing an edge would put a fact in the record
		// that no one observed. The writer matrix judges the *next* move,
		// whose `from` will be this To.
		if err := st.Record(ctx, t.ID, core.RecordedMove{To: t.State, Role: core.RoleControlPlane}); err != nil {
			return fmt.Errorf("state ingest: adopting %s: %w", t.Key, err)
		}
	}
	sort.Slice(adopted, func(i, j int) bool { return adopted[i].key < adopted[j].key })

	verb := "adopted"
	if *dryRun {
		verb = "would adopt (dry run, nothing written)"
	}
	fmt.Printf("%s %d ticket(s); %d already had a record and were left alone\n", verb, len(adopted), known)
	for _, a := range adopted {
		fmt.Printf("  %-10s %s\n", a.key, a.state)
	}
	summarize(func(w io.Writer) {
		summaryHeading(w, "State ingest")
		fmt.Fprintf(w, "%s **%d** ticket(s). **%d** already had a record and were left alone — a ticket the pipeline has moved carries real provenance, and overwriting it would erase the one fact the record holds.\n\n",
			verb, len(adopted), known)
		if len(adopted) == 0 {
			fmt.Fprintln(w, "Nothing to adopt.")
			return
		}
		fmt.Fprint(w, "Adopted where they stand. This is a starting line, not a correction: a ticket a human moved somewhere it should not be is adopted there, and the invariants apply to what happens next.\n\n")
		fmt.Fprintln(w, "| Ticket | State |")
		fmt.Fprintln(w, "|---|---|")
		for _, a := range adopted {
			fmt.Fprintf(w, "| %s | `%s` |\n", a.key, a.state)
		}
	})
	return nil
}
