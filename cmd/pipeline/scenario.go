package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/scenario"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
	"github.com/SwaggerAllen/orchestration/internal/tracker/linear"
)

func cmdScenario(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("scenario: want a subcommand: reset, seed, check, validate")
	}
	switch args[0] {
	case "reset":
		return cmdScenarioReset(args[1:])
	case "seed":
		return cmdScenarioSeed(args[1:])
	case "check":
		return cmdScenarioCheck(args[1:])
	case "validate":
		return cmdScenarioValidate(args[1:])
	default:
		return fmt.Errorf("scenario: unknown subcommand %q", args[0])
	}
}

// scenarioDeps builds the tracker. Deliberately no host: the harness
// reads the repository from disk, where the workflow has already
// checked it out.
func scenarioDeps(cfgPath string) (tracker.Tracker, *config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return nil, nil, fmt.Errorf("LINEAR_API_KEY is not set")
	}
	return linear.New(apiKey), cfg, nil
}

func cmdScenarioValidate(args []string) error {
	fs := flag.NewFlagSet("scenario validate", flag.ContinueOnError)
	dir := fs.String("dir", "scenarios", "directory of scenario files to validate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(*dir, "*.json"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("scenario validate: no scenarios in %s", *dir)
	}
	for _, p := range paths {
		s, err := scenario.Load(p)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s — %d ticket(s), %d expectation(s)\n",
			filepath.Base(p), s.Name, len(s.Tickets),
			len(s.Expect.FinalStates)+len(s.Expect.Files))
	}
	return nil
}

func cmdScenarioReset(args []string) error {
	fs := flag.NewFlagSet("scenario reset", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	confirm := fs.String("confirm", "", "the tracker project id, repeated back — reset archives every ticket in it")
	mergedOut := fs.String("merged-out", "", "write the archived tickets' merge commits here, for the repo half of the reset")
	// Required, and deliberately not defaulted to the config's directory
	// the way `check --repo` is. A wrong path there loses every file
	// expectation and the run says so; a wrong path here finds no retro
	// notes, which is indistinguishable from a project that has never
	// reached a milestone boundary — the silent half of the bug this
	// flag exists to close.
	repo := fs.String("repo", "", "checkout of the project repo, for the retro notes that record what an archived milestone merged")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" {
		return fmt.Errorf("scenario reset: --repo is required — the retro notes in the project checkout are the only record of what a milestone boundary archived")
	}
	t, cfg, err := scenarioDeps(*cfgPath)
	if err != nil {
		return err
	}
	// Checked before listing anything, so a reset aimed at a real
	// project fails having read nothing and touched nothing.
	if err := scenario.Guard(cfg, *confirm); err != nil {
		return err
	}
	res, err := scenario.Reset(context.Background(), t, cfg, *confirm, *repo, os.Stdout)
	if err != nil {
		return err
	}
	// Written even when empty. The repo step distinguishes "this
	// rehearsal merged nothing" from "the tracker step never ran", and
	// it can only do that if the file exists either way.
	if *mergedOut != "" {
		raw, err := json.MarshalIndent(res.Merges, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*mergedOut, append(raw, '\n'), 0o644); err != nil {
			return err
		}
	}
	// The two counts are reported separately, and neither is derived from
	// the other. An empty project used to end the story here — "nothing
	// was archived" was printed and the run stopped reading — but a
	// milestone boundary archives the tickets itself, so zero tickets and
	// a full merge list is exactly what a rehearsal that reached one
	// looks like.
	summarize(func(w io.Writer) {
		summaryHeading(w, "Rehearsal reset")
		if res.Archived == 0 {
			fmt.Fprintln(w, "No live tickets to archive — the project was already empty, or a milestone boundary archived them.")
		} else {
			fmt.Fprintf(w, "Archived **%d** ticket(s). Milestones were left in place. Tickets are archived rather than deleted — Linear keeps them recoverable.\n",
				res.Archived)
		}
		fmt.Fprintf(w, "\n**%d** merge commit(s) for the repo half to revert, **%d** of them from retro notes (work a milestone boundary had already archived).\n",
			len(res.Merges), res.FromNotes)
	})

	fmt.Printf("archived %d ticket(s); %d merge commit(s) to revert (%d from retro notes); milestones left in place\n",
		res.Archived, len(res.Merges), res.FromNotes)
	return nil
}

func cmdScenarioSeed(args []string) error {
	fs := flag.NewFlagSet("scenario seed", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	path := fs.String("scenario", "", "scenario file to seed")
	out := fs.String("out", "", "where to write the ref-to-key map the check phase reads")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || *out == "" {
		return fmt.Errorf("scenario seed: --scenario and --out are required")
	}
	s, err := scenario.Load(*path)
	if err != nil {
		return err
	}
	t, cfg, err := scenarioDeps(*cfgPath)
	if err != nil {
		return err
	}
	// Seeding is not destructive, but it is only ever aimed at a
	// rehearsal project, and pointing it at a real one would litter.
	if !cfg.Disposable {
		return scenario.ErrNotDisposable{ProjectID: cfg.Tracker.ProjectID}
	}
	seeded, err := scenario.Seed(context.Background(), t, cfg, s, os.Stdout)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(seeded, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("seeded %d ticket(s) for %q; wrote %s\n", len(seeded.Keys), s.Name, *out)

	// The keys are the thing to keep: from here the metronome, the
	// webhooks and the agents carry the tickets, and watching that
	// happen means knowing which tickets to watch. Nothing else in the
	// run tells you — the tracker's own list mixes them with whatever
	// the last rehearsal left behind.
	summarize(func(w io.Writer) {
		summaryHeading(w, "Seeded — "+s.Name)
		refs := make([]string, 0, len(seeded.Keys))
		for ref := range seeded.Keys {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			fmt.Fprintf(w, "- `%s` → **%s**\n", ref, seeded.Keys[ref])
		}
		fmt.Fprint(w, "\nThe metronome and the webhooks take it from here. Your touchpoints are the designed ones: sign off at `Design review`, and the boundary pass. When it settles, run this workflow again with `phase=check`.\n")
	})
	return nil
}

func cmdScenarioCheck(args []string) error {
	fs := flag.NewFlagSet("scenario check", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	path := fs.String("scenario", "", "scenario file to check against")
	seededPath := fs.String("seeded", "", "the map written by scenario seed")
	repo := fs.String("repo", "", "checkout of the project repo, for file expectations (default: the config file's directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || *seededPath == "" {
		return fmt.Errorf("scenario check: --scenario and --seeded are required")
	}
	s, err := scenario.Load(*path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*seededPath)
	if err != nil {
		return err
	}
	var seeded scenario.Seeded
	if err := json.Unmarshal(raw, &seeded); err != nil {
		return err
	}
	// A check run against a different scenario's seed map would compare
	// refs to keys that mean nothing and report confident nonsense.
	if seeded.Scenario != s.Name {
		return fmt.Errorf("scenario check: --seeded was written for %q but --scenario is %q", seeded.Scenario, s.Name)
	}
	t, cfg, err := scenarioDeps(*cfgPath)
	if err != nil {
		return err
	}
	// Same reasoning as the audit's --root: the project is where its
	// config is, not where the process stands. This one fails loudly
	// rather than passing vacuously — every file expectation misses —
	// but a rehearsal that reports the wrong reason is still a rehearsal
	// nobody can act on.
	if *repo == "" {
		*repo = cfg.Root
	}
	hasFile := func(p string) bool {
		_, err := os.Stat(filepath.Join(*repo, p))
		return err == nil
	}
	failures, err := scenario.Check(context.Background(), t, cfg, s, &seeded, hasFile)
	if err != nil {
		return err
	}
	// The rehearsal's verdict, and the one output of this whole workflow
	// a human is waiting on. It is read hours after the seed, often from
	// a phone, to answer one question: did the run do what the scenario
	// said it would.
	summarize(func(w io.Writer) {
		summaryHeading(w, "Rehearsal check — "+s.Name)
		if len(failures) == 0 {
			fmt.Fprintln(w, "**All expectations met.**")
			return
		}
		fmt.Fprintf(w, "**%d unmet expectation(s).**\n\n", len(failures))
		for _, f := range failures {
			fmt.Fprintln(w, "- "+f.String())
		}
	})

	if len(failures) == 0 {
		fmt.Printf("%s: all expectations met\n", s.Name)
		return nil
	}
	fmt.Printf("%s: %d unmet expectation(s)\n", s.Name, len(failures))
	for _, f := range failures {
		fmt.Println("  " + f.String())
	}
	return fmt.Errorf("scenario check failed")
}
