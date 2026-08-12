// Command pipeline is the control plane CLI. All pipeline logic lives here
// rather than in workflow YAML, so the same code path runs on a laptop
// against fakes, in CI against a scratch project, and in production
// (PLAN §1). Workflows are thin shells that call this binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/deploy/digitalocean"
	"github.com/SwaggerAllen/orchestration/internal/deploy/ghdeploy"
	"github.com/SwaggerAllen/orchestration/internal/host/github"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/setup"
	"github.com/SwaggerAllen/orchestration/internal/sim"
	"github.com/SwaggerAllen/orchestration/internal/tracker/linear"
)

// version is stamped by the release build; "dev" otherwise.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pipeline:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "setup":
		return cmdSetup(args[1:])
	case "sweep":
		return cmdSweep(args[1:])
	case "agent":
		return cmdAgent(args[1:])
	case "audit":
		return cmdAudit(args[1:])
	case "ids":
		return cmdIDs(args[1:])
	case "sim":
		return cmdSim(args[1:])
	case "scenario":
		return cmdScenario(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	default:
		return usageError()
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `usage: pipeline <command> [flags]

commands:
  setup    provision a Linear team with the pipeline's states and labels
           (idempotent; requires LINEAR_API_KEY)
  sweep    one control-plane pass: build snapshot, plan, apply
           (requires LINEAR_API_KEY; --dry-run plans without applying;
           PIPELINE_KILL_SWITCH=true halts all planning)
  agent    run-harness protocol steps: claim, finish, abort
           (used by the agent workflows, not by hand)
  audit    mutex audit for CI: changed paths vs the screen and system
           file maps and the ticket's labels (DESIGN 9)
  ids      print the Linear ids a config needs: viewer, teams, projects
           (requires LINEAR_API_KEY)
  sim      run a Ring-2 scenario against in-memory fakes (no network)
  scenario Ring-3 rehearsal against a disposable project: reset, seed,
           check, validate (requires LINEAR_API_KEY)
  version  print the binary version
`)
}

func usageError() error {
	usage(os.Stderr)
	return fmt.Errorf("unknown or missing command")
}

func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	dryRun := fs.Bool("dry-run", false, "print the plan without applying it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("setup: LINEAR_API_KEY is not set")
	}

	actions, err := setup.Run(context.Background(), linear.New(apiKey), cfg, *dryRun)
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		fmt.Println("nothing to do: team is already provisioned")
		return nil
	}
	verb := "applied"
	if *dryRun {
		verb = "planned (dry run, nothing applied)"
	}
	fmt.Printf("%d actions %s:\n", len(actions), verb)
	for _, a := range actions {
		fmt.Println("  " + a.String())
	}
	return nil
}

func cmdSweep(args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	dryRun := fs.Bool("dry-run", false, "plan and print actions without applying them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("sweep: LINEAR_API_KEY is not set")
	}
	kill := os.Getenv("PIPELINE_KILL_SWITCH")
	killOn := kill == "1" || kill == "true"

	p := plane.New(linear.New(apiKey), cfg)
	// With GitHub credentials present (always true inside Actions), the
	// snapshot gains run and CI facts and dispatches become real.
	repo, token := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo != "" && token != "" {
		h, err := github.New(repo, token)
		if err != nil {
			return err
		}
		p.WithHost(h)
		switch cfg.Deploy.Provider {
		case "github":
			d, err := ghdeploy.New(repo, token, cfg.Deploy.Endpoint)
			if err != nil {
				return err
			}
			p.WithDeploy(d)
		case "digitalocean":
			if doToken := os.Getenv("DIGITALOCEAN_TOKEN"); doToken != "" {
				p.WithDeploy(digitalocean.New(cfg.Deploy.Endpoint, doToken))
			} else {
				// Without the token Merged tickets stay pending and the
				// deploy timeout is the honest backstop; say so rather
				// than silently narrowing the sweep.
				fmt.Fprintln(os.Stderr, "pipeline: DIGITALOCEAN_TOKEN not set; deploy detection off, Merged tickets will hit the timeout")
			}
		}
	}
	ctx := context.Background()
	snap, err := p.Build(ctx, time.Now(), killOn)
	if err != nil {
		return err
	}
	acts := core.Sweep(snap)
	if killOn {
		fmt.Println("kill switch is on: nothing planned, nothing applied")
		return nil
	}
	if len(acts) == 0 {
		fmt.Printf("nothing to do (%d tickets read, milestone %q)\n", len(snap.Tickets), snap.CurrentMilestone)
		return nil
	}
	if *dryRun {
		fmt.Printf("%d actions planned (dry run, nothing applied):\n", len(acts))
		for _, a := range acts {
			fmt.Println("  " + a.String())
		}
		return nil
	}
	fmt.Printf("%d actions:\n", len(acts))
	return p.Execute(ctx, acts, os.Stdout)
}

func cmdIDs(args []string) error {
	fs := flag.NewFlagSet("ids", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ids: LINEAR_API_KEY is not set")
	}
	id, err := linear.New(apiKey).Whoami(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("viewer: %s  (%s)\n", id.ViewerID, id.ViewerName)
	fmt.Println("  ^ this id belongs in actors.controlplane (the key the control plane writes with),")
	fmt.Println("    or actors.author if this is your personal key")
	for _, t := range id.Teams {
		fmt.Printf("team: %s  [%s] %s\n", t.ID, t.Key, t.Name)
		for _, p := range t.Projects {
			fmt.Printf("  project: %s  %s\n", p.ID, p.Name)
		}
	}
	return nil
}

func cmdSim(args []string) error {
	fs := flag.NewFlagSet("sim", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to a project config (any valid config; the sim never contacts its services)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("sim: want exactly one scenario file, got %d args", fs.NArg())
	}

	cfg := config.Sample()
	if *cfgPath != "" {
		loaded, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		cfg = loaded
	}

	sc, err := sim.Load(fs.Arg(0))
	if err != nil {
		return err
	}
	res, err := sim.Run(context.Background(), sc, cfg)
	if err != nil {
		return err
	}
	fmt.Printf("scenario %q passed: %d steps\n", res.Scenario, res.StepsRun)
	return nil
}
