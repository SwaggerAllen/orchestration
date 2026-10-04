package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/host/github"
	"github.com/SwaggerAllen/orchestration/internal/manualtest"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
	"github.com/SwaggerAllen/orchestration/internal/tracker/linear"
)

// cmdManualVerdict records one judge pass's verdict as a check run
// (§8.3 rule 7), applying rule 3's evidence requirement and rule 8's
// disagreement rule.
//
// **Runs inside the judge job, under that job's own GITHUB_TOKEN**, and
// nothing else may call it: a fine-grained PAT cannot be granted the
// check-runs API at all, which is why host.CheckRuns is not part of
// host.Host and why this builds its own client rather than taking the
// plane's.
//
// **The exit code is the gate, and a disagreement does not fail.** §8.1
// routes a failing gate through the machinery that exists — CI red, the
// failure comment, first on the branch to Ready for rework and second to
// Blocked (§12). The sweep reads CI as workflow runs, so this job's exit
// code is what it sees. A rule 8 disagreement must therefore exit zero:
// it is a finding about the judge, and failing here would spend the
// ticket's escalation budget on work that was never broken, which is the
// exact thing rule 8 exists to prevent.
func cmdManualVerdict(args []string) error {
	fs := flag.NewFlagSet("manual-verdict", flag.ContinueOnError)
	cfgPath := fs.String("config", "pipeline.config.json", "path to the project config")
	sha := fs.String("sha", "", "the commit judged")
	testID := fs.String("test", "", "the manual test's id (its filename without .md)")
	verdict := fs.String("verdict", "", "pass | fail")
	title := fs.String("title", "", "the judge's one-line conclusion")
	summaryPath := fs.String("summary-file", "", "file holding the judge's reasoning")
	evidence := fs.String("evidence-url", "", "the run whose artifacts hold the evidence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, v := range map[string]string{"--sha": *sha, "--test": *testID, "--verdict": *verdict} {
		if v == "" {
			return fmt.Errorf("manual-verdict: %s is required", name)
		}
	}
	var passed bool
	switch *verdict {
	case "pass":
		passed = true
	case "fail":
	default:
		return fmt.Errorf("manual-verdict: --verdict is pass or fail, not %q", *verdict)
	}

	summary := ""
	if *summaryPath != "" {
		raw, err := os.ReadFile(*summaryPath)
		if err != nil {
			return err
		}
		summary = strings.TrimSpace(string(raw))
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	repo, token := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo == "" || token == "" {
		return fmt.Errorf("manual-verdict: GITHUB_REPOSITORY and GITHUB_TOKEN must both be set — " +
			"this runs inside the judge job, under that job's own token, because no fine-grained token " +
			"can be granted the check-runs API")
	}
	gh, err := github.New(repo, token)
	if err != nil {
		return err
	}

	ctx := context.Background()
	prior, err := gh.CheckRunsFor(ctx, *sha, manualtest.NamePrefix)
	if err != nil {
		return err
	}

	cr, disagreement := manualtest.Record(manualtest.Verdict{
		TestID: *testID, SHA: *sha, Passed: passed, Title: *title, Summary: summary, EvidenceURL: *evidence,
	}, prior)
	if err := gh.CreateCheckRun(ctx, cr); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", cr.Name, cr.Conclusion)

	if disagreement != nil {
		fmt.Printf("judge disagreement: %s\n", disagreement)
		if err := fileJudgeFinding(ctx, cfg, *disagreement, *evidence); err != nil {
			// Not fatal, and the reason is the whole of rule 8: the
			// check run is already the durable record, and failing the
			// job over an unfiled finding would turn a finding about
			// the judge into a failure of the ticket — which is what
			// exiting non-zero means here.
			fmt.Fprintf(os.Stderr, "manual-verdict: the finding was recorded as a check run but not filed: %v\n", err)
		}
		// Zero. See the doc comment.
		return nil
	}
	if cr.Conclusion == host.CheckRunFailure {
		return fmt.Errorf("manual-verdict: %s failed — %s", cr.Name, cr.Title)
	}
	return nil
}

// fileJudgeFinding puts a rule 8 disagreement into Triage as a `harness`
// proposal, which is the vocabulary the boundary pass already files
// findings about the machinery under (DESIGN §10).
//
// Filed rather than only recorded because a neutral check run is
// invisible unless somebody goes looking at that commit, and a judge that
// disagrees with itself is work — on the judge, not on the ticket.
//
// A missing LINEAR_API_KEY is a skip that says so, never a silence: the
// distinction every other check in this binary draws between "there was
// nothing to do" and "I could not tell".
func fileJudgeFinding(ctx context.Context, cfg *config.Config, d manualtest.Disagreement, evidenceURL string) error {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("LINEAR_API_KEY not set, so nothing was filed to Triage")
	}
	body := fmt.Sprintf(`The manual test judge reached two different verdicts for one test on one commit.

- test: %s
- commit: %s
- recorded earlier: %s (%s)
- this pass: %s (%s)

This is a finding about the judge rather than about the code, so the verdict on
that commit is recorded neutral and does not count toward the two failures that
block a ticket (ops-free-pipeline.md §8.3 rule 8, DESIGN §12). Nothing was
re-run: a third pass would be a third opinion rather than a tie-break.

What to look at: whether the test's expected observations are stated precisely
enough to be decided the same way twice, and whether the preview the judge read
was in the same state for both passes.`,
		d.TestID, d.SHA, d.Prior, d.PriorDetailsURL, d.Now, evidenceURL)

	tr := linear.New(apiKey)
	_, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID:      cfg.Tracker.TeamID,
		ProjectID:   cfg.Tracker.ProjectID,
		Title:       fmt.Sprintf("Judge disagreed with itself on %s", d.TestID),
		Description: body,
		Labels:      []string{protocol.ProposalLabels["harness"]},
	})
	return err
}
