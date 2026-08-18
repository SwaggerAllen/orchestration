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
	milestone := fs.String("milestone", "", "scope to one milestone by name (default: every milestone)")
	// Accepted and inert: spanning every milestone is now the default,
	// and project stubs carrying the old input would fail to parse a
	// flag that had simply been deleted. A stub lives in a project repo
	// and updates on its own schedule; the binary it calls tracks main.
	_ = fs.Bool("all", false, "deprecated: spanning every milestone is the default")
	format := fs.String("format", "text", "text, or markdown for a GitHub step summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "text" && *format != "markdown" {
		return fmt.Errorf("order: unknown --format %q; want text or markdown", *format)
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
	// Every milestone by default; --milestone narrows.
	//
	// It used to default to the current milestone, on the reasoning that
	// milestones are worked in sequence and a later one's tickets are
	// commonly filed with no dependencies, so a project-wide answer
	// would put them in "Ready now" beside work that can start today.
	// That reasoning is sound and it is still enforced — those tickets
	// are gated into the last layer with the reason attached — but it
	// belonged in the layering rather than in the scope. As a default
	// scope it also dropped every ticket accepted out of Triage without
	// a milestone yet assigned: startable work the queue will take
	// regardless of what this prints, absent from the one report whose
	// job is naming what to start.
	//
	// Asking for a milestone by name asks what is in it; asking for
	// nothing asks what can be started. Those are different questions
	// and only the second one has a useful default.
	want := *milestone
	o := core.ComputeOrder(snap, want)
	if *format == "markdown" {
		printOrderMarkdown(os.Stdout, o, want)
		return nil
	}
	printOrder(os.Stdout, o, want)
	// In Actions the report is the reason the run exists, so it goes
	// where it can be read without opening a step — in the markdown
	// form, since that is where the keys become tappable links.
	summarize(func(w io.Writer) { printOrderMarkdown(w, o, want) })
	return nil
}

func printOrder(w io.Writer, o *core.Order, milestone string) {
	scope := "every milestone"
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
			if t.Milestone != "" {
				fmt.Fprintf(w, "    state: %s   milestone: %s\n", t.State, t.Milestone)
			} else {
				fmt.Fprintf(w, "    state: %s\n", t.State)
			}
			if t.Note != "" {
				fmt.Fprintf(w, "    note:  %s\n", t.Note)
			}
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

// printOrderMarkdown renders the same report for a GitHub step summary,
// which is where this command is read when nobody is at a terminal.
//
// It is not a prettier text mode. The text form leans on alignment and a
// terminal's monospace column; a summary is proportional, often on a
// phone, and the keys have to be links or the reader is back to typing
// ticket numbers into a tracker search — which is the errand the report
// exists to spare them.
func printOrderMarkdown(w io.Writer, o *core.Order, milestone string) {
	scope := "every milestone"
	if milestone != "" {
		scope = "milestone " + milestone
	}
	fmt.Fprintf(w, "## Ordering — %s\n\n", scope)
	fmt.Fprintf(w, "Derived from the ticket graph as it stands now — nothing here is stored, so re-run it after anything moves.\n")

	for _, l := range o.Layers {
		fmt.Fprintf(w, "\n### %s\n\n%s\n\n", l.Name, upperFirst(l.Why))
		if len(l.Tickets) == 0 {
			fmt.Fprintln(w, "_(none)_")
			continue
		}
		for _, t := range l.Tickets {
			fmt.Fprintf(w, "**[%s · %s](%s)** — `%s`", t.Key, t.Title, t.URL, t.State)
			if t.Milestone != "" {
				fmt.Fprintf(w, " · milestone %s", t.Milestone)
			}
			fmt.Fprintln(w)
			if t.Note != "" {
				fmt.Fprintf(w, "- note: %s\n", t.Note)
			}
			for _, n := range t.BlockedBy {
				fmt.Fprintf(w, "- blocked by `%s` %s _(%s)_\n", n.Key, n.Title, n.State)
			}
			for _, n := range t.Blocks {
				fmt.Fprintf(w, "- blocks `%s` %s _(%s)_\n", n.Key, n.Title, n.State)
			}
			for _, m := range t.MutexHeldBy {
				fmt.Fprintf(w, "- mutex: `%s` is in flight and holds `%s` — designing this now is legal, but it cannot be promoted until that lands\n", m.Key, m.Label)
			}
			fmt.Fprintln(w)
		}
	}
}

// upperFirst starts the layer's explanation as a sentence. The text form
// prints it after an em dash, where lowercase is right; on its own line
// under a heading it is not.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func neighbours(ns []core.Neighbour) string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, fmt.Sprintf("%s (%s) %s", n.Key, n.State, n.Title))
	}
	return strings.Join(out, "\n                ")
}
