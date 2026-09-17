package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/host/github"
)

// previewPendingExit is the exit code for "not yet". Distinct from 1 so a
// caller polling can tell "come back" from "this is broken" — a loop that
// treats every non-zero the same either gives up on a preview that was
// still building or waits out a failure that was never going to arrive.
const previewPendingExit = 3

// cmdPreviewURL prints where a branch's preview is, for a workflow step
// that needs to drive it (ops-free-pipeline.md §8, DESIGN §4).
//
// Read from the code host rather than built from the branch name: the
// platform reports its own preview as a deployment on the PR, and
// deriving the URL would be guessing at somebody else's slugging and
// handing the guess to a judge as the thing under test.
func cmdPreviewURL(args []string) error {
	fs := flag.NewFlagSet("preview-url", flag.ContinueOnError)
	branch := fs.String("branch", "", "the branch whose preview to find")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *branch == "" {
		return fmt.Errorf("preview-url: --branch is required")
	}
	repo, token := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo == "" || token == "" {
		return fmt.Errorf("preview-url: GITHUB_REPOSITORY and GITHUB_TOKEN must both be set")
	}
	gh, err := github.New(repo, token)
	if err != nil {
		return err
	}
	pv, err := gh.PreviewFor(context.Background(), *branch)
	if err != nil {
		return err
	}
	switch pv.Status {
	case host.PreviewReady:
		fmt.Println(pv.URL)
		return nil
	case host.PreviewPending:
		// Pending and absent both exit 3, deliberately. A caller polling
		// cannot act on the difference — "the platform has taken the
		// branch" and "nothing has claimed it yet" are both "not yet",
		// and a platform that creates its deployment late would
		// otherwise make the first poll a hard failure.
		fmt.Fprintf(os.Stderr, "preview-url: %s is still building%s\n", *branch, reason(pv))
		os.Exit(previewPendingExit)
	case host.PreviewFailed:
		return fmt.Errorf("preview-url: %s's preview failed%s", *branch, reason(pv))
	default:
		fmt.Fprintf(os.Stderr, "preview-url: nothing has deployed %s yet\n", *branch)
		os.Exit(previewPendingExit)
	}
	return nil
}

func reason(pv host.Preview) string {
	if pv.Description == "" {
		return ""
	}
	return ": " + pv.Description
}
