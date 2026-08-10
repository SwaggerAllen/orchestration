package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/agent"
	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/host/github"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/tracker/linear"
)

func cmdAgent(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("agent: want a subcommand: claim, finish, abort")
	}
	switch args[0] {
	case "claim":
		return cmdAgentClaim(args[1:])
	case "finish":
		return cmdAgentFinish(args[1:])
	case "abort":
		return cmdAgentAbort(args[1:])
	default:
		return fmt.Errorf("agent: unknown subcommand %q", args[0])
	}
}

// agentDeps builds the plane and host from the environment the Actions
// runner already has: LINEAR_API_KEY, GITHUB_TOKEN, GITHUB_REPOSITORY.
func agentDeps(cfgPath string) (*plane.Plane, host.Host, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return nil, nil, fmt.Errorf("LINEAR_API_KEY is not set")
	}
	repo, token := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo == "" || token == "" {
		return nil, nil, fmt.Errorf("GITHUB_REPOSITORY and GITHUB_TOKEN are required for agent runs")
	}
	h, err := github.New(repo, token)
	if err != nil {
		return nil, nil, err
	}
	return plane.New(linear.New(apiKey), cfg).WithHost(h), h, nil
}

func cmdAgentClaim(args []string) error {
	fs := flag.NewFlagSet("agent claim", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	ticket := fs.String("ticket", "", "ticket key, e.g. PIPE-12")
	dispatchID := fs.String("dispatch-id", "", "workflow run id (the claim's audit trail)")
	dispatchURL := fs.String("dispatch-url", "", "workflow run URL")
	outDir := fs.String("out", "", "directory for claim.json, scope.md and prompt.md")
	promptTemplate := fs.String("prompt-template", "", "agent base prompt file to assemble prompt.md from")
	handbackPath := fs.String("handback-path", "", "path the model must write its hand-back to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ticket == "" || *outDir == "" || *dispatchID == "" {
		return fmt.Errorf("agent claim: --ticket, --dispatch-id and --out are required")
	}
	p, _, err := agentDeps(*cfgPath)
	if err != nil {
		return err
	}

	res, err := agent.Claim(context.Background(), p, *ticket, *dispatchID, *dispatchURL, time.Now())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "claim.json"), raw, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "scope.md"), []byte(res.Scope), 0o644); err != nil {
		return err
	}
	if *promptTemplate != "" {
		tpl, err := os.ReadFile(*promptTemplate)
		if err != nil {
			return err
		}
		prompt := assemblePrompt(string(tpl), res, *handbackPath)
		if err := os.WriteFile(filepath.Join(*outDir, "prompt.md"), []byte(prompt), 0o644); err != nil {
			return err
		}
	}
	// Step outputs for the workflow's later steps.
	if ghOut := os.Getenv("GITHUB_OUTPUT"); ghOut != "" {
		f, err := os.OpenFile(ghOut, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		fmt.Fprintf(f, "branch=%s\npr_number=%d\nmode=%s\n", res.Branch, res.PRNumber, res.Mode)
	}
	fmt.Printf("claimed %s (%s): branch %s, PR #%d\n", res.TicketKey, res.Mode, res.Branch, res.PRNumber)
	return nil
}

// assemblePrompt joins the base prompt with the ticket context. The
// template is the protocol half; this is the per-run half. Everything the
// model must treat as work-to-judge is fenced under explicit headings so
// the base prompt can point at it (DESIGN §9's trust boundary).
func assemblePrompt(template string, res *agent.ClaimResult, handbackPath string) string {
	var b []byte
	add := func(s string) { b = append(b, s...) }
	add(template)
	add("\n\n---\n\n")
	add(fmt.Sprintf("# Ticket %s: %s\n\nMode: %s\n", res.TicketKey, res.Title, res.Mode))
	if res.Mode == "rework" {
		add("\nThis ticket bounced. The SCOPE below is the newest comment — implement exactly that.\nRe-implementing the original description re-lands work that already merged (DESIGN §2.3).\n")
	}
	add("\n## Scope — implement exactly this\n\n" + res.Scope + "\n")
	if res.Mode == "rework" && res.Description != "" {
		add("\n## Original argument (context only — do not re-implement)\n\n" + res.Description + "\n")
	}
	add("\n## Base check\n\n")
	if res.BaseSHA != "" {
		add(fmt.Sprintf("The design was drawn against `%s`. Diff it against origin/main; if main moved and both changes touch the same behavior, DO NOT reconcile by guessing — abort with a push-back (DESIGN §2.4).\n", res.BaseSHA))
	} else {
		add("No Base sha recorded on this ticket. Note that in your hand-back.\n")
	}
	add(fmt.Sprintf("\n## Mechanics\n\n- Work on branch `%s` (already checked out).\n- Commit your work with clear messages; the harness pushes.\n", res.Branch))
	if handbackPath != "" {
		add(fmt.Sprintf("- Write your hand-back to `%s` before you finish: what landed, the commit, anything deliberately not done and why, any open question you resolved (DESIGN vocabulary: Hand-back).\n", handbackPath))
	}
	return string(b)
}

func loadClaim(path string) (*agent.ClaimResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var res agent.ClaimResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &res, nil
}

func cmdAgentFinish(args []string) error {
	fs := flag.NewFlagSet("agent finish", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	claimPath := fs.String("claim", "", "claim.json written by agent claim")
	handback := fs.String("handback", "", "file containing the hand-back comment")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *claimPath == "" || *handback == "" {
		return fmt.Errorf("agent finish: --claim and --handback are required")
	}
	p, h, err := agentDeps(*cfgPath)
	if err != nil {
		return err
	}
	res, err := loadClaim(*claimPath)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(*handback)
	if err != nil {
		return fmt.Errorf("agent finish: reading hand-back: %w (an issue that moves without one is a state change nobody can audit)", err)
	}
	if err := agent.Finish(context.Background(), p, h, res, string(body)); err != nil {
		return err
	}
	fmt.Printf("finished %s: PR #%d ready, ticket in Checks\n", res.TicketKey, res.PRNumber)
	return nil
}

func cmdAgentAbort(args []string) error {
	fs := flag.NewFlagSet("agent abort", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	claimPath := fs.String("claim", "", "claim.json written by agent claim")
	reason := fs.String("reason", "failed", "pushback or failed")
	message := fs.String("message", "", "the argument (required for pushback)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *claimPath == "" {
		return fmt.Errorf("agent abort: --claim is required (a run that never claimed has nothing to abort)")
	}
	p, _, err := agentDeps(*cfgPath)
	if err != nil {
		return err
	}
	res, err := loadClaim(*claimPath)
	if err != nil {
		return err
	}
	if err := agent.Abort(context.Background(), p, res.TicketID, *reason, *message); err != nil {
		return err
	}
	fmt.Printf("aborted %s (%s)\n", res.TicketKey, *reason)
	return nil
}
