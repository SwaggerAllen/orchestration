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
	"github.com/SwaggerAllen/orchestration/internal/deploy"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/host/github"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
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

// deployCredential names, per provider, what preflight calls the deploy
// check and the credential or permission it exercises. Labels only: a
// provider missing an entry is a blank column in one report, never deploy
// detection silently off, which is why this is a map rather than a second
// copy of deployPort's switch. It lives here, beside its only reader,
// because TestPreflightProbesEveryScopeTheSnapshotNeeds reads the scope
// strings out of this package's source and a scope spelled in a file that
// builds no checks would satisfy it while probing nothing.
var deployCredential = map[string][2]string{
	"github":       {"the deployment list", "deployments: read"},
	"digitalocean": {"the app's deployments", "DIGITALOCEAN_TOKEN"},
	"render":       {"the service's deploys", "RENDER_API_KEY"},
}

// hostChecks is every probe that needs the code host, the deploy port
// behind the last of them, and — when there is no port — the reason.
//
// Extracted from cmdPreflight so the deploy branch is testable. It is the
// only conditional in the list: the five host checks are appended
// unconditionally, while the deploy one depends on the provider and on a
// credential, and a probe built and then not appended is a check the
// report never mentions. Reverting the append printed `ok` while
// deployCheck itself was covered — the shape CLAUDE.md records for
// awaitingDispatchOf, where a test asserted the end state and not that
// anything ran.
func hostChecks(gh host.Host, cfg *config.Config, repo, token string) ([]check, deploy.Deploy, string, error) {
	checks := []check{
		// Reads one listing per wired agent workflow, so it now
		// checks the `agents` map as well as the scope: a renamed
		// or mistyped filename 404s here, named, instead of
		// surfacing later as an agent kind that looks permanently
		// idle. Preflight is the one workflow the author can run
		// from a phone, which is where a config typo is worth
		// costing a line rather than a run.
		check{"the agent run list, per wired workflow", "actions: read", func(ctx context.Context) error {
			_, err := gh.ListAgentRuns(ctx)
			return err
		}},
		check{"the open PR list", "pull-requests: read", func(ctx context.Context) error {
			_, err := gh.ListOpenPRs(ctx)
			return err
		}},
		check{"CI verdicts per PR head", "actions: read", func(ctx context.Context) error {
			sha, err := headSHA(ctx, gh)
			if err != nil {
				return err
			}
			_, err = gh.ChecksFor(ctx, sha)
			return err
		}},
		// A different endpoint from the PR list, and that is the
		// point: the list response does not carry `mergeable` at
		// all, so this is the single-PR GET and it can fail on its
		// own. With no open PR there is nothing to ask about, which
		// is a skip rather than a pass.
		check{"a PR's merge state", "pull-requests: read", func(ctx context.Context) error {
			prs, err := gh.ListOpenPRs(ctx)
			if err != nil {
				return err
			}
			if len(prs) == 0 {
				return nil
			}
			_, err = gh.MergeStateFor(ctx, prs[0].Number)
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
	}
	dc, d, why, err := deployCheck(cfg, repo, token)
	if err != nil {
		return nil, nil, "", err
	}
	if d == nil {
		return checks, nil, why, nil
	}
	return append(checks, dc), d, "", nil
}

// deployCheck builds the preflight probe for the configured deploy
// provider: the check, the port to attach to the plane, and — when there
// is no port — the reason.
//
// The port comes from deployPort rather than from a switch here, so
// preflight probes the client the sweep will actually use. A switch did
// sit here for a milestone, which is the hazard deployPort's own comment
// names: adding the render case to one of them left preflight unable to
// probe a Render project at all, printed as a project with no deploy
// configured rather than as a gap.
func deployCheck(cfg *config.Config, repo, token string) (check, deploy.Deploy, string, error) {
	d, why, err := deployPort(cfg, repo, token)
	if err != nil || d == nil {
		return check{}, nil, why, err
	}
	label := deployCredential[cfg.Deploy.Provider]
	return check{label[0], label[1], func(ctx context.Context) error {
		_, err := d.State(ctx)
		return err
	}}, d, "", nil
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
		// Diffed against the protocol, like the label check below, and
		// for the same reason: this one held the configured names and
		// the live table in one hand each and compared neither. It
		// asserted the team had *some* states, which is true of every
		// Linear team ever created.
		//
		// The two halves it now checks are not equally novel, and the
		// difference is worth keeping straight so neither gets
		// simplified out for the wrong reason.
		//
		// The **missing-name** half overlaps `plane.resolveStates`,
		// which already refuses to build a snapshot without every
		// mapped state and names the first one it meets. Kept anyway,
		// because it reports every missing state at once — the posture
		// the composer and the audit already take — and because it runs
		// first here while the snapshot check runs last, so "run setup"
		// arrives before the slow credential checks rather than after
		// them.
		//
		// The **category** half is checked nowhere else at runtime.
		// `setup` compares it and then deliberately refuses to retype a
		// live state, so a category that drifted stays drifted and
		// nothing says so on any later run: the name still resolves, the
		// pipeline still writes to it, and the category is what decides
		// which states are triage (`plane.Build`) and how a
		// resolved-but-unmapped state is rescued. Wrong there is wrong
		// quietly, which is the shape this check exists to catch.
		check{"the state table", "LINEAR_API_KEY", func(ctx context.Context) error {
			live, err := tr.ListStates(ctx, cfg.Tracker.TeamID)
			if err != nil {
				return err
			}
			return stateTableProblems(cfg, live)
		}},
		// Diffed, not merely fetched. This check used to assert only
		// that the call returned — vacuous with respect to the set, with
		// the protocol's labels and the live list both in hand and
		// nothing compared. A label the pipeline writes can therefore
		// ship in one commit while the team it writes to was provisioned
		// before that commit, and nothing re-runs setup on an upgrade.
		// `harness` did exactly that: it entered protocol.Labels in the
		// same commit that taught the filer to emit it, and surfaced two
		// days later mid-boundary, twice, at eleven minutes of model
		// spend each, as `no label "harness" in team ...`. The adjacent
		// state check already did better by failing with "run setup".
		check{"the label set", "LINEAR_API_KEY", func(ctx context.Context) error {
			live, err := tr.ListLabels(ctx, cfg.Tracker.TeamID)
			if err != nil {
				return err
			}
			have := make(map[string]bool, len(live))
			for _, l := range live {
				have[l.Name] = true
			}
			var missing []string
			for _, want := range protocol.Labels {
				if !have[want] {
					missing = append(missing, want)
				}
			}
			if len(missing) > 0 {
				return fmt.Errorf("team is missing %d label(s) the pipeline writes: %s — run `pipeline setup --apply`, which creates them idempotently",
					len(missing), strings.Join(missing, ", "))
			}
			return nil
		}},
	)

	repo, token := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo == "" || token == "" {
		fmt.Println("GITHUB_REPOSITORY/GITHUB_TOKEN not set — host and deploy checks skipped.")
	} else {
		gh, err := github.New(repo, token, github.WithAgentWorkflows(cfg.AgentWorkflows()))
		if err != nil {
			return err
		}
		p.WithHost(gh)
		hcs, d, why, err := hostChecks(gh, cfg, repo, token)
		if err != nil {
			return err
		}
		// Unasserted, and knowingly: cmdPreflight builds its host client
		// from the environment, so nothing test-side can run this body.
		// Reverting this line still prints `ok`. It is one unconditional
		// append covering every host check rather than a per-provider
		// branch — which is why the branch moved into hostChecks and this
		// did not. Closing it means injecting the host into the command.
		checks = append(checks, hcs...)
		if d == nil {
			fmt.Printf("%s — deploy detection off; Merged tickets will reach the deploy timeout.\n", why)
		} else {
			p.WithDeploy(d)
		}
	}

	// The state store, if the project has one. Not folded into the
	// snapshot check below because a missing token and an unreachable
	// Worker fail there as one opaque error, and they are different
	// fixes — and because "this project has no store" is a legitimate
	// answer that has to be told apart from "its store is broken".
	if st := stateStore(cfg); st != nil {
		checks = append(checks, check{"the pipeline's move record", "PIPELINE_STATE_TOKEN", func(ctx context.Context) error {
			_, err := st.All(ctx)
			return err
		}})
		p.WithState(st)
	} else {
		fmt.Println("no state store configured — transitions are unrecorded and the DESIGN 9 invariants are not enforced.")
	}

	// The whole snapshot last: it is what every command starts with, and
	// it fails on things no single call above can see — an unmapped
	// state, a milestone the config names and the tracker doesn't.
	checks = append(checks, check{"a full snapshot build", "all of the above", func(ctx context.Context) error {
		_, err := p.Build(ctx, time.Now(), false)
		return err
	}})

	// Printed as they run — some of these are slow, and a reader
	// watching a live log wants the failures as they land. Collected as
	// well, because the summary can only be written once the last one
	// is in.
	type result struct {
		check
		err error
	}
	results := make([]result, 0, len(checks))
	failed := 0
	for _, c := range checks {
		err := c.run(ctx)
		results = append(results, result{c, err})
		if err != nil {
			failed++
			fmt.Printf("FAIL  %-28s needs %-22s %v\n", c.name, c.scope, err)
			continue
		}
		fmt.Printf("ok    %-28s (%s)\n", c.name, c.scope)
	}

	// The summary matters more here than anywhere else in the CLI: this
	// command is run by someone who suspects a permission is wrong, and
	// the answer they need is which line of which workflow to edit.
	// Putting that on the run page means they never open a step.
	summarize(func(w io.Writer) {
		summaryHeading(w, "Preflight")
		if failed > 0 {
			fmt.Fprintf(w, "**%d of %d checks failed.** Fix these before a ticket is in flight, not after — a missing scope surfaces at whatever moment first needs it, which is generally the worst one.\n\n", failed, len(checks))
		} else {
			fmt.Fprintf(w, "**All %d checks green.**\n\n", len(checks))
		}
		fmt.Fprintln(w, "| | Check | Needs | Result |")
		fmt.Fprintln(w, "|---|---|---|---|")
		for _, r := range results {
			if r.err != nil {
				fmt.Fprintf(w, "| ❌ | %s | `%s` | %s |\n", r.name, r.scope, mdCell(r.err.Error()))
				continue
			}
			fmt.Fprintf(w, "| ✅ | %s | `%s` | |\n", r.name, r.scope)
		}
		// On a green run especially: "all green" is the moment a reader
		// is most likely to conclude the permissions are fine, and this
		// is the half of the sentence that is not covered.
		fmt.Fprintf(w, "\n### Not exercised\n\nThese are writes, and a diagnostic that leaves litter is one nobody runs. A green preflight means *the reads are fine*, not *the permissions are fine* — the most expensive gap found so far was a write.\n\n")
		fmt.Fprintln(w, "| Scope | Needed for |")
		fmt.Fprintln(w, "|---|---|")
		for _, u := range unexercised {
			fmt.Fprintf(w, "| `%s` | %s |\n", u.scope, u.why)
		}
	})

	if failed > 0 {
		return fmt.Errorf("preflight: %d of %d checks failed — fix these before a ticket is in flight, not after", failed, len(checks))
	}
	fmt.Printf("preflight: %d checks, all green\n", len(checks))
	reportUnexercised()
	return nil
}

// mdCell makes an error safe to drop into a table cell: a pipe ends the
// cell early and a newline ends the row, and API errors carry both.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.Join(strings.Fields(s), " ")
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
	// The one entry here that preflight could not probe even if it were
	// willing to leave litter: no fine-grained token can be granted the
	// check-runs API at all, so this is unexercisable rather than merely
	// unexercised. Named anyway, because the list's job is to keep the
	// green line honest about what a passing preflight has not
	// established — and "could not have" is exactly the case a reader
	// would otherwise assume was covered.
	{"checks: write", "recording a manual test verdict, from the judge job's own token — preflight cannot probe this at all"},
	{"LINEAR_API_KEY (write)", "every transition, comment and label the harness makes"},
	{"PIPELINE_STATE_TOKEN (write)", "recording each move before making it, which is what makes the invariants enforceable"},
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

// stateTableProblems compares the team's states against the protocol.
//
// Extracted from the check above so it can be tested against a table
// rather than a live team: a guard nobody has watched fail is a guard
// nobody has tested, and the credential-bearing wrapper cannot be run
// without a tracker.
//
// All problems at once, which is the posture the composer and the audit
// already take for the same reason — a run that fixes the first and
// re-runs to find the second is a run spent learning what one pass could
// have said.
func stateTableProblems(cfg *config.Config, live []tracker.StateInfo) error {
	if len(live) == 0 {
		// Short-circuited rather than reported per state: an
		// unprovisioned team is one fix, and spelling it as sixteen
		// missing states buries that under a wall.
		return fmt.Errorf("team has no states at all — run `pipeline setup --apply`")
	}
	byName := make(map[string]tracker.StateInfo, len(live))
	for _, s := range live {
		byName[s.Name] = s
	}
	var problems []string
	for _, ps := range protocol.AllStates {
		name := cfg.StateName(ps)
		have, ok := byName[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%q (%s) is missing", name, ps))
			continue
		}
		if want := protocol.Categories[ps]; have.Category != want {
			problems = append(problems, fmt.Sprintf("%q (%s) has category %q, protocol wants %q", name, ps, have.Category, want))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the team's states disagree with the protocol in %d place(s): %s — "+
			"`pipeline setup --apply` creates a missing state idempotently; it will not retype a live one, "+
			"so a category mismatch is yours to resolve in Linear",
			len(problems), strings.Join(problems, "; "))
	}
	return nil
}
