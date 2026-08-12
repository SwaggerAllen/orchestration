package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if err := FinishDesign(ctx, p, h, res, o); err != nil {
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
	if err := FinishDesign(ctx, p, h, res, o); err != nil {
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
	if err := FinishDesign(ctx, p, h, res, o); err != nil {
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
	if err := FinishDesign(ctx, p, h, res2, o2); err != nil {
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

// The claim is the only place the non-asks can enter a design run: the
// model has no tracker credentials and is not getting any (DESIGN §9).
// If this stops happening the run doesn't fail — it quietly proposes
// against decisions the author already made.
func TestDesignClaimCarriesTheNonAsksDocument(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	tr.AddDocument(cfg.Tracker.ProjectID, cfg.NonAsksDocument, "- No dark mode: two palettes, one designer.")
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.Designing)

	res, err := ClaimDesign(ctx, p, i.Key, "run_54", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.NonAsks == nil {
		t.Fatal("claim carries no non-asks at all")
	}
	if !res.NonAsks.Found || !strings.Contains(res.NonAsks.Body, "No dark mode") {
		t.Errorf("non-asks = %+v, want the project's document", res.NonAsks)
	}
}

// A project without the document still claims, and the result says so
// explicitly rather than arriving nil — the prompt distinguishes "none
// recorded" from "could not read", and it can only do that if the claim
// reports which one it was.
func TestDesignClaimReportsAnAbsentNonAsksDocument(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.Designing)

	res, err := ClaimDesign(ctx, p, i.Key, "run_55", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.NonAsks == nil || res.NonAsks.Found || res.NonAsks.Title != cfg.NonAsksDocument {
		t.Errorf("non-asks = %+v, want a not-found record naming %q", res.NonAsks, cfg.NonAsksDocument)
	}
}
