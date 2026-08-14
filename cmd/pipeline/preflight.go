package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/deploy"
	"github.com/SwaggerAllen/orchestration/internal/deploy/digitalocean"
	"github.com/SwaggerAllen/orchestration/internal/deploy/ghdeploy"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/host/github"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/tracker/linear"
)

// Preflight exercises every credential-bearing call the pipeline makes,
// on demand, against a live project.
//
// It exists because permission gaps here do not surface where they are
// introduced. They surface at whatever moment first needs the scope,
// which for the deploy read was "the first ticket ever to reach Merged"
// — three weeks and four rehearsals after the scope became necessary,
// with a ticket mid-flight and the sweep dying before it could produce a
// single action. Three separate 403s landed that way; each was one line
// of YAML and each cost a stranded ticket to find.
//
// The point is that every call runs on every invocation, whether or not
// the project currently happens to have a ticket in the state that would
// trigger it. A check you only reach by luck is the thing this replaces.
//
// Reads only, and it says so. Every write the pipeline makes has a
// side effect somebody would have to undo — a dispatched run, an opened
// PR, a recorded deployment, a moved ticket — and a diagnostic that
// leaves litter is one nobody runs. The cost is real and named rather
// than hidden: the most expensive 403 of the lot was reconcile's
// POST /deployments, a write, and a green preflight would not have
// caught it. So the write scopes are listed as unexercised at the end,
// where a reader deciding "am I safe now" can see the half that was not
// checked.
//
// Nothing runs this on a schedule or as a step in another job. It is a
// thing you invoke when something is wrong, or once when wiring a
// project up.
type check struct {
	name  string // what the pipeline calls it for
	scope string // the permission it needs, as the workflow spells it
	run   func(context.Context) error
}

func cmdPreflight(args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	ctx := context.Background()

	var checks []check

	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("preflight: LINEAR_API_KEY is not set — nothing to check without it")
	}
	tr := linear.New(apiKey)
	p := plane.New(tr, cfg)
	checks = append(checks,
		check{"the state table", "LINEAR_API_KEY", func(ctx context.Context) error {
			states, err := tr.ListStates(ctx, cfg.Tracker.TeamID)
			if err != nil {
				return err
			}
			if len(states) == 0 {
				return fmt.Errorf("team has no states — run setup")
			}
			return nil
		}},
		check{"the label set", "LINEAR_API_KEY", func(ctx context.Context) error {
			_, err := tr.ListLabels(ctx, cfg.Tracker.TeamID)
			return err
		}},
	)

	repo, token := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo == "" || token == "" {
		fmt.Println("GITHUB_REPOSITORY/GITHUB_TOKEN not set — host and deploy checks skipped.")
	} else {
		gh, err := github.New(repo, token)
		if err != nil {
			return err
		}
		p.WithHost(gh)
		checks = append(checks,
			check{"the agent run list", "actions: read", func(ctx context.Context) error {
				_, err := gh.ListAgentRuns(ctx)
				return err
			}},
			check{"the open PR list", "pull-requests: read", func(ctx context.Context) error {
				_, err := gh.ListOpenPRs(ctx)
				return err
			}},
			check{"CI verdicts per PR head", "checks: read", func(ctx context.Context) error {
				sha, err := headSHA(ctx, gh)
				if err != nil {
					return err
				}
				_, err = gh.ChecksFor(ctx, sha)
				return err
			}},
			check{"commit ancestry", "contents: read", func(ctx context.Context) error {
				sha, err := headSHA(ctx, gh)
				if err != nil {
					return err
				}
				_, err = gh.IsAncestor(ctx, sha, sha)
				return err
			}},
		)

		var d deploy.Deploy
		switch cfg.Deploy.Provider {
		case "github":
			gd, err := ghdeploy.New(repo, token, cfg.Deploy.Endpoint)
			if err != nil {
				return err
			}
			d = gd
			checks = append(checks, check{"the deployment list", "deployments: read", func(ctx context.Context) error {
				_, err := gd.State(ctx)
				return err
			}})
		case "digitalocean":
			if doToken := os.Getenv("DIGITALOCEAN_TOKEN"); doToken != "" {
				dd := digitalocean.New(cfg.Deploy.Endpoint, doToken)
				d = dd
				checks = append(checks, check{"the app's deployments", "DIGITALOCEAN_TOKEN", func(ctx context.Context) error {
					_, err := dd.State(ctx)
					return err
				}})
			} else {
				fmt.Println("DIGITALOCEAN_TOKEN not set — deploy detection off; Merged tickets will reach the deploy timeout.")
			}
		}
		if d != nil {
			p.WithDeploy(d)
		}
	}

	// The whole snapshot last: it is what every command starts with, and
	// it fails on things no single call above can see — an unmapped
	// state, a milestone the config names and the tracker doesn't.
	checks = append(checks, check{"a full snapshot build", "all of the above", func(ctx context.Context) error {
		_, err := p.Build(ctx, time.Now(), false)
		return err
	}})

	failed := 0
	for _, c := range checks {
		if err := c.run(ctx); err != nil {
			failed++
			fmt.Printf("FAIL  %-28s needs %-22s %v\n", c.name, c.scope, err)
			continue
		}
		fmt.Printf("ok    %-28s (%s)\n", c.name, c.scope)
	}
	if failed > 0 {
		return fmt.Errorf("preflight: %d of %d checks failed — fix these before a ticket is in flight, not after", failed, len(checks))
	}
	fmt.Printf("preflight: %d checks, all green\n", len(checks))
	reportUnexercised()
	return nil
}

// unexercised is every scope the pipeline needs that preflight does not
// probe, because probing it would mean making the write. Printed on a
// green run specifically: "all green" is the moment a reader is most
// likely to conclude the permissions are fine, and this is the half that
// sentence does not cover.
var unexercised = []struct{ scope, why string }{
	{"actions: write", "dispatching agent workflows"},
	{"contents: write", "pushing the agent's commits to the ticket branch"},
	{"pull-requests: write", "opening the PR and flipping the draft off"},
	{"deployments: write", "recording the stand-in deployment after a merge"},
	{"LINEAR_API_KEY (write)", "every transition, comment and label the harness makes"},
}

func reportUnexercised() {
	fmt.Println("\nnot exercised — these are writes, and a diagnostic that leaves litter is one nobody runs:")
	for _, u := range unexercised {
		fmt.Printf("  %-24s %s\n", u.scope, u.why)
	}
}

// headSHA picks a commit to probe the per-commit endpoints with. An open
// PR's head is the shape those calls really see; with no open PR the
// checks would otherwise be skipped on exactly the quiet repo where a
// preflight is most useful, so the deploy state's SHA stands in.
func headSHA(ctx context.Context, h host.Host) (string, error) {
	prs, err := h.ListOpenPRs(ctx)
	if err != nil {
		return "", err
	}
	if len(prs) > 0 {
		return prs[0].HeadSHA, nil
	}
	return "HEAD", nil
}
