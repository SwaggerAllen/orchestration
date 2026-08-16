// Command pipeline is the control plane CLI. All pipeline logic lives here
// rather than in workflow YAML, so the same code path runs on a laptop
// against fakes, in CI against a scratch project, and in production
// (PLAN §1). Workflows are thin shells that call this binary.
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
	"github.com/SwaggerAllen/orchestration/internal/deploy"
	"github.com/SwaggerAllen/orchestration/internal/deploy/digitalocean"
	"github.com/SwaggerAllen/orchestration/internal/deploy/ghdeploy"
	"github.com/SwaggerAllen/orchestration/internal/host/github"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/setup"
	"github.com/SwaggerAllen/orchestration/internal/sim"
	"github.com/SwaggerAllen/orchestration/internal/state"
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
	case "order":
		return cmdOrder(args[1:])
	case "preflight":
		return cmdPreflight(args[1:])
	case "audit":
		return cmdAudit(args[1:])
	case "ids":
		return cmdIDs(args[1:])
	case "sim":
		return cmdSim(args[1:])
	case "scenario":
		return cmdScenario(args[1:])
	case "state":
		return cmdState(args[1:])
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
  setup    provision a Linear team with the pipeline's states and labels,
           then probe deploy.endpoint so a wrong one surfaces at hookup
           rather than on the first ticket to reach Merged
           (idempotent; requires LINEAR_API_KEY)
  sweep    one control-plane pass: build snapshot, plan, apply
           (requires LINEAR_API_KEY; --dry-run plans without applying;
           PIPELINE_KILL_SWITCH=true halts all planning)
  agent    run-harness protocol steps: claim, finish, abort
           (used by the agent workflows, not by hand)
  order    what to start next: the ticket graph in layers, with each
           ticket's blockers and what it blocks. Scoped to the current
           milestone unless --milestone or --all (requires LINEAR_API_KEY)
  preflight exercise every credential-bearing call the pipeline makes,
           so a missing scope fails here rather than mid-ticket
           (requires LINEAR_API_KEY; GITHUB_TOKEN adds the host checks)
  audit    mutex audit for CI: changed paths vs the screen and system
           file maps and the ticket's labels (DESIGN 9)
  ids      print the Linear ids a config needs: viewer, teams, projects
           (requires LINEAR_API_KEY)
  sim      run a Ring-2 scenario against in-memory fakes (no network)
  scenario Ring-3 rehearsal against a disposable project: reset, seed,
           check, validate (requires LINEAR_API_KEY)
  state    the pipeline's own move record: ingest adopts a project's
           existing tickets so the DESIGN 9 invariants apply to them from
           now rather than from whenever each next moves
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
	verb := "applied"
	if *dryRun {
		verb = "planned (dry run, nothing applied)"
	}
	if len(actions) == 0 {
		fmt.Println("nothing to do: team is already provisioned")
	} else {
		fmt.Printf("%d actions %s:\n", len(actions), verb)
		for _, a := range actions {
			fmt.Println("  " + a.String())
		}
	}

	// Setup is run from a workflow precisely because the person running
	// it is not at a terminal, so the report of what changed has to be
	// somewhere they can see it. The deploy check below is the part
	// worth surfacing: it is the one that fails.
	deployErr := checkDeploy(context.Background(), cfg)
	summarize(func(w io.Writer) {
		summaryHeading(w, "Setup")
		if len(actions) == 0 {
			fmt.Fprint(w, "Nothing to do — the team is already provisioned.\n\n")
		} else {
			fmt.Fprintf(w, "**%d actions %s:**\n\n", len(actions), verb)
			for _, a := range actions {
				fmt.Fprintln(w, "- "+a.String())
			}
			fmt.Fprintln(w)
		}
		if deployErr != nil {
			fmt.Fprintf(w, "### Deploy check failed\n\nNothing exercises deploy detection again until a ticket reaches `Merged`, hours later, where the symptom is \"stuck in Merged\" and names neither the endpoint nor the token. So it is worth fixing now.\n\n```\n%v\n```\n", deployErr)
			return
		}
		if cfg.Deploy.Provider != "" {
			fmt.Fprintf(w, "Deploy endpoint `%s` answered.\n", cfg.Deploy.Endpoint)
		}
	})
	return deployErr
}

// deployPort builds the deploy port the config names, or returns nil plus
// the reason there is none. Sweep and setup share it deliberately: a check
// that constructs its own client can pass while the sweep's client fails,
// and then the check is worse than nothing.
func deployPort(cfg *config.Config, repo, token string) (deploy.Deploy, string, error) {
	switch cfg.Deploy.Provider {
	case "github":
		if repo == "" || token == "" {
			return nil, "GITHUB_REPOSITORY and GITHUB_TOKEN are not both set", nil
		}
		d, err := ghdeploy.New(repo, token, cfg.Deploy.Endpoint)
		if err != nil {
			return nil, "", err
		}
		return d, "", nil
	case "digitalocean":
		doToken := os.Getenv("DIGITALOCEAN_TOKEN")
		if doToken == "" {
			return nil, "DIGITALOCEAN_TOKEN not set", nil
		}
		return digitalocean.New(cfg.Deploy.Endpoint, doToken), "", nil
	}
	return nil, "", nil
}

// checkDeploy probes the configured deploy endpoint at hookup, because
// nothing else does until far too late. Every other piece of a project's
// wiring is exercised early — the tracker ids on the first sweep, the
// model credential on the first dispatch, the audit on the first PR — but
// deploy detection sits unused through design, dev, CI and reconcile, and
// then runs for the first time on the last hop of the first ticket. One
// project carried a wrong app id from hookup until that moment, and the
// symptom, hours later, was "stuck in Merged" — which names neither the
// endpoint nor the token.
//
// A failure here is fatal to the command on purpose: the provisioning
// above has already been applied and is idempotent, so the cost of exiting
// non-zero is a re-run, while the cost of a printed warning is that nobody
// reads it until a ticket is stuck.
func checkDeploy(ctx context.Context, cfg *config.Config) error {
	if cfg.Deploy.Provider == "" {
		return nil
	}
	d, why, err := deployPort(cfg, os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN"))
	if err != nil {
		return fmt.Errorf("setup: deploy check: %w", err)
	}
	if d == nil {
		// Not a pass. Say which credential was missing, so a skipped check
		// cannot be mistaken for a clean one.
		fmt.Printf("deploy check SKIPPED (%s): %s was never contacted\n", why, cfg.Deploy.Endpoint)
		return nil
	}
	if _, err := d.State(ctx); err != nil {
		return fmt.Errorf("setup: deploy check failed against %s: %w\n"+
			"  Nothing exercises deploy detection again until a ticket reaches Merged, so this is the moment to fix it.\n"+
			"  404: the endpoint names an app this token cannot see — a wrong app id, or a token minted in another team.\n"+
			"  401: the token is wrong, expired, or revoked", cfg.Deploy.Endpoint, err)
	}
	fmt.Printf("deploy check: %s answered\n", cfg.Deploy.Endpoint)
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

	p := plane.New(linear.New(apiKey), cfg).WithState(stateStore(cfg))
	// With GitHub credentials present (always true inside Actions), the
	// snapshot gains run and CI facts and dispatches become real.
	repo, token := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo != "" && token != "" {
		h, err := github.New(repo, token)
		if err != nil {
			return err
		}
		p.WithHost(h)
		d, why, err := deployPort(cfg, repo, token)
		if err != nil {
			return err
		}
		if d != nil {
			p.WithDeploy(d)
		} else if why != "" {
			// Without a port, Merged tickets stay pending and the deploy
			// timeout is the honest backstop; say so rather than silently
			// narrowing the sweep.
			fmt.Fprintln(os.Stderr, "pipeline: "+why+"; deploy detection off, Merged tickets will hit the timeout")
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
		summarize(func(w io.Writer) {
			summaryHeading(w, "Sweep — parked")
			fmt.Fprintln(w, "`PIPELINE_KILL_SWITCH` is on: the snapshot was read, nothing was planned and nothing was applied. Clear the repo variable to resume.")
		})
		return nil
	}
	if len(acts) == 0 {
		fmt.Printf("nothing to do (%d tickets read, milestone %q)\n", len(snap.Tickets), snap.CurrentMilestone)
		summarize(func(w io.Writer) {
			summaryHeading(w, "Sweep — nothing to do")
			fmt.Fprintf(w, "%d tickets read, milestone %s. Every ticket is either waiting on something outside the pipeline or already where it should be.\n",
				len(snap.Tickets), mdOrNone(snap.CurrentMilestone))
		})
		return nil
	}
	if *dryRun {
		fmt.Printf("%d actions planned (dry run, nothing applied):\n", len(acts))
		for _, a := range acts {
			fmt.Println("  " + a.String())
		}
		summarize(func(w io.Writer) {
			summaryHeading(w, fmt.Sprintf("Sweep — %d actions planned", len(acts)))
			fmt.Fprint(w, "Dry run; nothing was applied.\n\n")
			for _, a := range acts {
				fmt.Fprintln(w, "- "+a.String())
			}
		})
		return nil
	}
	fmt.Printf("%d actions:\n", len(acts))

	// Executed actions are teed rather than re-rendered from `acts`:
	// Execute stops at the first failure, so the plan and what actually
	// happened are different lists, and the summary has to show the
	// second one or it will claim work the tracker never saw.
	var done strings.Builder
	err = p.Execute(ctx, acts, teeTo(os.Stdout, &done))
	summarize(func(w io.Writer) {
		summaryHeading(w, fmt.Sprintf("Sweep — %d actions", len(acts)))
		for _, line := range strings.Split(strings.TrimSpace(done.String()), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				fmt.Fprintln(w, "- "+line)
			}
		}
		if err != nil {
			fmt.Fprintf(w, "\n**Stopped on an error.** Anything above this line was applied; anything in the plan below it was not.\n\n```\n%v\n```\n", err)
		}
	})
	return err
}

// mdOrNone renders an empty string as something a reader can tell apart
// from a value that happens to look blank.
func mdOrNone(s string) string {
	if s == "" {
		return "_(none)_"
	}
	return "**" + s + "**"
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

// stateStore builds the pipeline's record of its own writes, or nil.
//
// Nil is a supported configuration and not a degraded one: a project
// with no store records nothing, so the sweep judges nothing, which is
// exactly where a project sits before it is wired up and where every
// ticket that predates the store sits forever. The alternative — a
// store that silently answers "no records" — is the failure this whole
// mechanism exists to end, so it is never manufactured here.
//
// The URL comes from the config because it is the same Worker the
// metronome runs in and the project already names its deploy endpoint
// there; the token is a secret and comes from the environment.
func stateStore(cfg *config.Config) state.Store {
	st := state.NewWorker(cfg.State.URL, cfg.State.Project, os.Getenv("PIPELINE_STATE_TOKEN"))
	if st == nil {
		// Named rather than silent. A missing store is legitimate and a
		// missing store nobody noticed is how the invariants went quiet
		// the first time.
		fmt.Fprintln(os.Stderr, "pipeline: no state store configured — transitions are unrecorded and the §9 invariants are not enforced")
		return nil
	}
	return st
}
