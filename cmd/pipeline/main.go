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

	"github.com/SwaggerAllen/orchestration/internal/config"
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
	case "sim":
		return cmdSim(args[1:])
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
  sim      run a Ring-2 scenario against in-memory fakes (no network)
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
