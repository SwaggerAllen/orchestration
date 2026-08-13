package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

func TestDesignArtifactsFlow(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.Designing)

	res, err := ClaimDesign(ctx, p, i.Key, "run_50", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "design" {
		t.Fatalf("mode = %q", res.Mode)
	}

	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"home", "cap"}, Systems: []string{"caps"}, Summary: "Two states added; cap_reached carries the copy decision."}
	if err := FinishDesign(ctx, p, h, res, o, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.DesignReview {
		t.Errorf("state = %q, want design_review", got)
	}
	if len(h.PRs) != 1 || !h.PRs[0].Draft {
		t.Errorf("want one draft PR, got %+v", h.PRs)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	for _, want := range []string{"screen:home", "screen:cap", "system:caps"} {
		found := false
		for _, l := range issues[0].Labels {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("label %q missing: %v", want, issues[0].Labels)
		}
	}
}

func TestDesignDecisionlessAutoPass(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Backend index", "No surfaces.", protocol.Designing)

	res, err := ClaimDesign(ctx, p, i.Key, "run_51", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "decisionless", Systems: []string{"search"}, Summary: "No screens, no structural change; index work inside search."}
	if err := FinishDesign(ctx, p, h, res, o, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.ReadyForDev {
		t.Errorf("state = %q, want ready_for_dev", got)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	hasSystem := false
	for _, l := range issues[0].Labels {
		if l == "system:search" {
			hasSystem = true
		}
	}
	if !hasSystem {
		t.Error("decisionless pass must still attach system labels — touching is not deciding (DESIGN 4)")
	}
	// The marker must precede the transition in comment order — the sweep
	// judges the arrival by it (DESIGN §9).
	found := false
	for _, c := range issues[0].Comments {
		if strings.Contains(c.Body, "[pipeline:v1:decisionless-pass]") {
			found = true
		}
	}
	if !found {
		t.Error("decisionless-pass marker missing")
	}
	if len(h.PRs) != 0 {
		t.Errorf("decisionless pass must not open a PR: %+v", h.PRs)
	}
}

func TestDesignRereadClearAndDemote(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)

	flagged := seed(t, tr, cfg, "Flagged", "d", protocol.ReadyForDev)
	if err := tr.AddIssueLabel(ctx, cfg.Tracker.TeamID, flagged.ID, "re-evaluate"); err != nil {
		t.Fatal(err)
	}
	res, err := ClaimDesign(ctx, p, flagged.Key, "run_52", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "design-reread" {
		t.Fatalf("mode = %q", res.Mode)
	}
	o := &DesignOutcome{Outcome: "clear", Summary: "The colliding ticket rewrote a different region; this scope still holds."}
	if err := FinishDesign(ctx, p, h, res, o, ""); err != nil {
		t.Fatal(err)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	for _, l := range issues[0].Labels {
		if l == "re-evaluate" {
			t.Error("clear must remove the flag")
		}
	}
	if got := issueState(t, tr, cfg, flagged.ID); got != protocol.ReadyForDev {
		t.Errorf("clear must hold state, got %q", got)
	}

	demoted := seed(t, tr, cfg, "Demoted", "d", protocol.ReadyForRework)
	if err := tr.AddIssueLabel(ctx, cfg.Tracker.TeamID, demoted.ID, "re-evaluate"); err != nil {
		t.Fatal(err)
	}
	res2, err := ClaimDesign(ctx, p, demoted.Key, "run_53", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o2 := &DesignOutcome{Outcome: "demote", Summary: "The ground moved under this scope; it needs a fresh pass."}
	if err := FinishDesign(ctx, p, h, res2, o2, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, demoted.ID); got != protocol.Designing {
		t.Errorf("demote: state = %q, want designing", got)
	}
}

func TestLoadDesignOutcomeValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "o.json")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadDesignOutcome(write(`{"outcome":"clear","summary":"x"}`), "design"); err == nil {
		t.Error("re-read outcomes must be illegal in design mode")
	}
	if _, err := LoadDesignOutcome(write(`{"outcome":"decisionless","screens":["home"],"summary":"x"}`), "design"); err == nil {
		t.Error("decisionless with screens is a contradiction")
	}
	if _, err := LoadDesignOutcome(write(`{"outcome":"demote","summary":""}`), "design-reread"); err == nil {
		t.Error("demote without its argument must be rejected")
	}
	if o, err := LoadDesignOutcome(write(`{"outcome":"artifacts","screens":["home"]}`), "design"); err != nil || o.Outcome != "artifacts" {
		t.Errorf("artifacts without summary should load, got %v %v", o, err)
	}
}

// The non-asks live in the project repo beside the screen and system
// docs, and the claim inlines them: a prompt whose most important input
// is "go read this file" is a prompt whose most important input is
// optional.
func TestDesignClaimCarriesTheNonAsksDocument(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeNonAsks(t, cfg, "- No dark mode: two palettes, one designer.")
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.Designing)

	res, err := ClaimDesign(ctx, p, i.Key, "run_54", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.NonAsks == nil {
		t.Fatal("claim carries no non-asks at all")
	}
	if !res.NonAsks.Found || !strings.Contains(res.NonAsks.Body, "No dark mode") {
		t.Errorf("non-asks = %+v, want the repo's document", res.NonAsks)
	}
}

// A project without the file still claims, and the result says so
// explicitly rather than arriving nil — the prompt distinguishes "none
// recorded" from "could not read", and it can only do that if the claim
// reports which one it was.
func TestDesignClaimReportsAnAbsentNonAsksDocument(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	cfg.Root = t.TempDir()
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.Designing)

	res, err := ClaimDesign(ctx, p, i.Key, "run_55", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.NonAsks == nil || res.NonAsks.Found || res.NonAsks.Err != "" {
		t.Errorf("non-asks = %+v, want a clean not-found record", res.NonAsks)
	}
	if res.NonAsks.Path != cfg.NonAsksPath {
		t.Errorf("path = %q, want %q — the agent writes new entries there", res.NonAsks.Path, cfg.NonAsksPath)
	}
}

// The design agent runs with cwd inside the pipeline checkout, not the
// project. A path resolved against cwd finds nothing on every real run,
// and the failure is silent: the prompt would report "the repo records
// none" while the file sat right there in the project.
func TestDesignClaimReadsTheNonAsksFromTheProjectNotTheWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeNonAsks(t, cfg, "- No dark mode.")
	t.Chdir(t.TempDir()) // stand somewhere else entirely, as a real run does
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.Designing)

	res, err := ClaimDesign(ctx, p, i.Key, "run_56", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !res.NonAsks.Found {
		t.Error("found = false; the file is in the project, which is where the claim must look")
	}
}

func writeNonAsks(t *testing.T, cfg *config.Config, body string) {
	t.Helper()
	if err := os.WriteFile(cfg.InRoot(cfg.NonAsksPath), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Design review is the author reading the rendered states (DESIGN §4),
// and the ticket used to arrive in that state without saying where they
// were — the URL had to be rebuilt by hand from a branch name and a
// Pages project every time.
func TestDesignPostsThePreviewLinkBeforeAskingForReview(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.Designing)

	res, err := ClaimDesign(ctx, p, i.Key, "run_60", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}, Summary: "Two states."}
	const url = "https://abc123.orchestration-dummy.pages.dev"
	if err := FinishDesign(ctx, p, h, res, o, url); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.DesignReview {
		t.Fatalf("state = %q", got)
	}

	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	var previewAt = -1
	for n, c := range issues[0].Comments {
		m, ok, err := marker.Parse(c.Body)
		if err == nil && ok && m.Kind == marker.Preview {
			previewAt = n
			if m.Fields["url"] != url {
				t.Errorf("preview marker url = %q, want %q", m.Fields["url"], url)
			}
			if !strings.Contains(c.Body, url) {
				t.Error("the prose must carry the link too — the marker is for the machine, the link is for the author")
			}
		}
	}
	if previewAt < 0 {
		t.Fatal("no preview marker on a ticket being sent to Design review")
	}
}

// A project with no preview wired gets silence rather than a broken
// link, and the pass still lands.
func TestDesignWithoutAPreviewPostsNoLink(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.Designing)

	res, err := ClaimDesign(ctx, p, i.Key, "run_61", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := FinishDesign(ctx, p, h, res, &DesignOutcome{Outcome: "artifacts", Summary: "s"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.DesignReview {
		t.Errorf("state = %q, want design_review", got)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	for _, c := range issues[0].Comments {
		if m, ok, err := marker.Parse(c.Body); err == nil && ok && m.Kind == marker.Preview {
			t.Error("posted a preview marker with no preview")
		}
	}
}
