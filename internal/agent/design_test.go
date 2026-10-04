package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/changespec"
	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

func TestDesignArtifactsFlow(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)

	res, err := ClaimDesign(ctx, p, i.Key, "run_50", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "design" {
		t.Fatalf("mode = %q", res.Mode)
	}

	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"home", "cap"}, Systems: []string{"caps"}, Summary: "Two states added; cap_reached carries the copy decision."}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
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
	i := seed(t, tr, cfg, "Backend index", "No surfaces.", protocol.ReadyForDesign)

	res, err := ClaimDesign(ctx, p, i.Key, "run_51", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "decisionless", Systems: []string{"search"}, Summary: "No screens, no structural change; index work inside search."}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
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
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
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
	if err := FinishDesign(ctx, p, h, res2, o2, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, demoted.ID); got != protocol.ReadyForRedesign {
		t.Errorf("demote: state = %q, want the redesign queue", got)
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
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)

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
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)

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
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)

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
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)

	res, err := ClaimDesign(ctx, p, i.Key, "run_60", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}, Summary: "Two states."}
	const url = "https://abc123.orchestration-dummy.pages.dev"
	if err := FinishDesign(ctx, p, h, res, o, url, "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
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
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)

	res, err := ClaimDesign(ctx, p, i.Key, "run_61", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := FinishDesign(ctx, p, h, res, &DesignOutcome{Outcome: "artifacts", Summary: "s"}, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
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

// The base sha is the one thing in the description's schema that nothing
// could ever write: DESIGN §4 names the description as its home and
// §2.3 makes descriptions immutable, so the only writer that could fill
// it in is the one forbidden to edit it. Every ticket therefore reported
// "no Base sha recorded" and the §2.4 concurrency check never ran. The
// harness posts it as a marker at design finish, and the claim reads it
// back.
func TestDesignFinishRecordsTheBaseSHA(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument, with no Base line", protocol.ReadyForDesign)

	// DesignOwnedPaths as a real claim carries it: the ownership audit
	// reads the claim, not the config, and this pass now changes a file.
	res := &ClaimResult{TicketID: i.ID, TicketKey: i.Key, Title: i.Title, Branch: "b", DesignOwnedPaths: cfg.DesignOwnedPaths}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}}
	if err := FinishDesign(ctx, p, h, res, o, "", "abc1234", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
		t.Fatal(err)
	}

	// Read it back the way a dev claim does.
	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	var tk *core.Ticket
	for _, cand := range snap.Tickets {
		if cand.ID == i.ID {
			tk = cand
		}
	}
	if tk == nil {
		t.Fatal("ticket vanished")
	}
	if got := baseSHA(tk); got != "abc1234" {
		t.Errorf("baseSHA = %q, want abc1234 — the dev pass is back to having nothing to diff against", got)
	}
}

// A hand-written Base: line in the description still works. It is what
// DESIGN §4 documents, and an author who writes one means it.
func TestBaseSHAFallsBackToTheDescription(t *testing.T) {
	tk := &core.Ticket{Description: "The argument.\n\nBase: deadbeef\n\nWhat it touches: ..."}
	if got := baseSHA(tk); got != "deadbeef" {
		t.Errorf("baseSHA = %q, want deadbeef", got)
	}
	if got := baseSHA(&core.Ticket{Description: "no base here"}); got != "" {
		t.Errorf("baseSHA = %q, want empty", got)
	}
}

// The label and the doc filename are one string in two places: CI
// derives the label it requires from the filename, and the design pass
// declares the name the label is made from. Nothing compared them.
//
// Catapult's ORC-5 measured the cost. A pass declared `core-dsl` where
// the doc is `systems/core_dsl.md`, and `system:core-dsl` — a real
// label, taking part in the mutex — survived design, the author's
// sign-off and a full dev run (22 modules, 60 tests, three commits)
// before surfacing as 28 audit violations on every path the ticket was
// about. The only outcome left to the run was a push-back asking a
// human to rename a label.
func TestDesignRefusesATouchListNamingADocThatDoesNotExist(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeDoc(t, cfg, "systems", "core_dsl", "lib/dsl/**")
	writeDoc(t, cfg, "screens", "home", "lib/web/home/**")
	i := seed(t, tr, cfg, "The loader", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_60", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	o := &DesignOutcome{Outcome: "artifacts", Summary: "s", Systems: []string{"core-dsl"}}
	err = FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, "")
	if err == nil {
		t.Fatal("a touch list naming a doc that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "system:core_dsl") {
		t.Errorf("the refusal does not name the spelling that would work: %v", err)
	}
	// And it refuses before anything moves: a ticket that advanced under
	// an unsatisfiable label is the failure being prevented.
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Designing {
		t.Errorf("state = %q, want the ticket left where it was", got)
	}

	// The correct spelling passes, and so does a screen beside it.
	o = &DesignOutcome{Outcome: "artifacts", Summary: "s", Systems: []string{"core_dsl"}, Screens: []string{"home"}}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
		t.Fatalf("a touch list naming real docs was refused: %v", err)
	}
}

// Every bad name at once. A pass that declared two should not have to be
// re-run to learn about the second.
func TestDesignReportsEveryUnknownDocAtOnce(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeDoc(t, cfg, "systems", "core_dsl", "lib/dsl/**")
	i := seed(t, tr, cfg, "The loader", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_61", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	o := &DesignOutcome{Outcome: "artifacts", Summary: "s", Systems: []string{"core-dsl", "foundation"}}
	err = FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, "")
	if err == nil {
		t.Fatal("unknown docs were accepted")
	}
	for _, want := range []string{"core-dsl", "foundation"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// A project that has not written its first system doc declares nothing
// the audit can require, so a name there constrains nothing and cannot
// fail CI. Refusing would break those projects over a label that is
// inert — the check has to be silent exactly where it has no subject.
func TestDesignAcceptsAnyNameWhenTheProjectHasNoDocs(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	cfg.Root = t.TempDir()
	i := seed(t, tr, cfg, "The loader", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_62", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Summary: "s", Systems: []string{"anything"}}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
		t.Fatalf("a project with no system docs was refused: %v", err)
	}
}

func writeDoc(t *testing.T, cfg *config.Config, dir, name, glob string) {
	t.Helper()
	d := filepath.Join(cfg.Root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\npaths:\n  - " + glob + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(d, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// State-transition-as-claim, now that design has a queue to be claimed
// out of. The ORC-7 failure in one assertion: a claim that never
// completes leaves the ticket in the queue, not in a state asserting an
// agent is working on it.
func TestDesignClaimMovesTheTicketOutOfTheQueue(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)

	if got := issueState(t, tr, cfg, i.ID); got != protocol.ReadyForDesign {
		t.Fatalf("fixture is in %q", got)
	}
	res, err := ClaimDesign(ctx, p, i.Key, "run_90", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Designing {
		t.Errorf("state = %q, want the claim to have taken it out of the queue", got)
	}
	if res.State != protocol.Designing {
		t.Errorf("claim reports state %q, want the state it just wrote", res.State)
	}
	if res.Mode != "design" {
		t.Errorf("mode = %q, want a normal pass", res.Mode)
	}

	// And a second run cannot claim what the first one took: the queue
	// is empty, so the pickup assertion refuses rather than two agents
	// working one ticket.
	if _, err := ClaimDesign(ctx, p, i.Key, "run_91", "u", time.Now()); err == nil {
		t.Error("a second design run claimed a ticket already in Designing")
	} else if !core.Refused(err) {
		t.Errorf("the refusal is not marked as one, so it would park the ticket: %v", err)
	}
}

// A re-evaluate re-read is design looking at a ticket that belongs to
// the dev queue. It must not move it — the ticket is not being designed,
// it is being re-read in place (DESIGN §7).
func TestDesignRereadLeavesTheDevQueueAlone(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDev)
	if err := p.AddTicketLabel(ctx, i.ID, core.LabelReEvaluate); err != nil {
		t.Fatal(err)
	}

	res, err := ClaimDesign(ctx, p, i.Key, "run_92", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "design-reread" {
		t.Errorf("mode = %q, want a re-read", res.Mode)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.ReadyForDev {
		t.Errorf("state = %q, want the dev queue untouched", got)
	}
}

// A design pass that wrote outside designOwnedPaths must not advance the
// ticket. Catapult's ORC-84 opened a draft PR carrying six implementation
// modules and asked for Design review on it; the pass had no boundary in
// its prompt and nothing checked the diff (DESIGN §5, §9).
func TestDesignFinishRefusesStraysOutsideOwnedPaths(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)

	res, err := ClaimDesign(ctx, p, i.Key, "run_90", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"home"}, Summary: "s"}
	changed := []string{
		"screens/home.md",
		"lib/sample/greetings.ex",
		"lib/sample_web/live/home_live.ex",
	}
	err = FinishDesign(ctx, p, h, res, o, "", "", append(designedSpec(t, cfg, res.TicketKey), changed...), nil, nil, "")
	if err == nil {
		t.Fatal("finish accepted a pass that committed implementation")
	}
	// Blocked is the abort step's job, not this one's — what has to hold
	// here is that the ticket did not reach Design review and no draft PR
	// invited the author to read the strays as design.
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Designing {
		t.Errorf("state = %q, want the ticket left where the pass held it", got)
	}
	if len(h.PRs) != 0 {
		t.Errorf("want no PR, got %+v", h.PRs)
	}
	// The finding goes on the ticket, naming every stray: the author
	// reads the comment, not the run log, and one aggregated finding is
	// what tells them the size of what to strip.
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	last := ""
	if cs := issues[0].Comments; len(cs) > 0 {
		last = cs[len(cs)-1].Body
	}
	for _, want := range []string{"lib/sample/greetings.ex", "lib/sample_web/live/home_live.ex", "proposal"} {
		if !strings.Contains(last, want) {
			t.Errorf("comment does not mention %q: %s", want, last)
		}
	}
	if strings.Contains(last, "screens/home.md") {
		t.Errorf("the owned path was reported as a stray: %s", last)
	}
}

// The ordinary pass: docs and components, all inside the owned paths.
func TestDesignFinishAcceptsOwnedPaths(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)

	res, err := ClaimDesign(ctx, p, i.Key, "run_91", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DesignOwnedPaths) == 0 {
		t.Fatal("the claim carried no boundary; the prompt has nothing to state and the finish nothing to hold")
	}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"home"}, Summary: "s"}
	changed := []string{
		"screens/home.md",
		"systems/greetings.md",
		"lib/sample_web/components/cap_banner.ex",
		"non-asks.md",
	}
	if err := FinishDesign(ctx, p, h, res, o, "", "", append(designedSpec(t, cfg, res.TicketKey), changed...), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.DesignReview {
		t.Errorf("state = %q, want design_review", got)
	}
}

// The dead end ORC-157 records: a pass that correctly finds its scope
// depends on something not on main had no legal outcome, so the run died
// and the ticket landed in Blocked under `failed` — the flavor that says
// the harness broke. It now parks deliberately, with its own flavor.
func TestDesignPrerequisiteParksTheTicket(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Delivery retries", "Depends on the queue doc.", protocol.ReadyForDesign)

	res, err := ClaimDesign(ctx, p, i.Key, "run_57", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const summary = "systems/queue.md is not on main yet; ORC-140 writes it. Nothing to draw against until that lands."
	o := &DesignOutcome{Outcome: "prerequisite", Summary: summary}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Blocked {
		t.Errorf("state = %q, want blocked", got)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	label := false
	for _, l := range issues[0].Labels {
		if l == core.LabelPrerequisite {
			label = true
		}
	}
	if !label {
		// Without the flavor the park is indistinguishable from a crash
		// in the one column the author triages from.
		t.Errorf("no %q label: %v", core.LabelPrerequisite, issues[0].Labels)
	}
	var blocked, argument bool
	for _, c := range issues[0].Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil {
			t.Fatal(err)
		}
		if ok && m.Kind == marker.Blocked && m.Fields["prerequisite"] == "1" {
			blocked = true
		}
		if strings.Contains(c.Body, summary) {
			argument = true
		}
	}
	if !blocked {
		t.Error("no blocked marker carrying prerequisite=1")
	}
	if !argument {
		t.Error("the pass's summary never reached the ticket — the person unparking it has nothing to go on")
	}
	if len(h.PRs) != 0 {
		t.Errorf("a pass that decided nothing must not open a PR: %+v", h.PRs)
	}
}

// A prerequisite park's argument has to survive into the next run's
// prompt. Markers are addresses, not arguments, so a `blocked` comment
// whose flavor `marker.Prose` does not recognise is dropped whole —
// which would tell the re-picked-up ticket that it was blocked and never
// why (DESIGN §9).
func TestThePrerequisiteArgumentSurvivesIntoThePrompt(t *testing.T) {
	m := marker.Marker{Kind: marker.Blocked, Fields: map[string]string{"prerequisite": "1", "from": "designing"}}
	body := m.Comment("systems/queue.md is not on main yet; ORC-140 writes it.")
	prose, worth := marker.Prose(body)
	if !worth || !strings.Contains(prose, "ORC-140") {
		t.Errorf("prose = %q, worth = %v — the park's argument was dropped as bookkeeping", prose, worth)
	}
}

// The mutex reads Blocked as in flight (core.isStarted), so a label
// attached by a pass that could not read its scope holds every other
// ticket naming that system out until a human moves this one. The touch
// list is refused rather than ignored: a pass that thought it was
// declaring one should learn it was not.
func TestPrerequisiteRefusesATouchList(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "o.json")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct{ name, json string }{
		{"systems", `{"outcome":"prerequisite","systems":["queue"],"summary":"x"}`},
		{"screens", `{"outcome":"prerequisite","screens":["home"],"summary":"x"}`},
	} {
		if _, err := LoadDesignOutcome(write(c.json), "design"); err == nil {
			t.Errorf("prerequisite with %s must be refused", c.name)
		}
	}
	if _, err := LoadDesignOutcome(write(`{"outcome":"prerequisite","summary":""}`), "design"); err == nil {
		t.Error("prerequisite without naming what it waits on is a ticket nobody can unpark")
	}
	if _, err := LoadDesignOutcome(write(`{"outcome":"prerequisite","summary":"x"}`), "design-reread"); err == nil {
		t.Error("a re-read ticket is already in the dev queue; `demote` is its way out, not this")
	}
	if o, err := LoadDesignOutcome(write(`{"outcome":"prerequisite","summary":"x"}`), "design"); err != nil || o.Outcome != "prerequisite" {
		t.Errorf("prerequisite must load in design mode, got %v %v", o, err)
	}
}

// ORC-158: the mutex labels were add-only. A pass that narrows its scope
// left the label the earlier pass took holding the mutex against every
// other ticket naming that system, and no pass could clear it — only a
// direct tracker write did. Catapult's ORC-141 sat on `system:delivery`
// that way.
func TestANarrowedPassReleasesTheLabelItNoLongerDeclares(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeDoc(t, cfg, "systems", "delivery", "lib/delivery/**")
	writeDoc(t, cfg, "systems", "engine", "lib/engine/**")
	i := seed(t, tr, cfg, "Retry policy", "The argument.", protocol.ReadyForDesign)
	for _, l := range []string{"system:delivery", "system:engine", "harness"} {
		if err := p.AddTicketLabel(ctx, i.ID, l); err != nil {
			t.Fatal(err)
		}
	}
	res, err := ClaimDesign(ctx, p, i.Key, "run_58", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// The narrowed pass: engine only, on a branch that touches engine
	// only. Nothing on it is mapped by systems/delivery.md.
	o := &DesignOutcome{Outcome: "artifacts", Systems: []string{"engine"}, Summary: "Retries belong in the engine after all."}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), []string{"lib/engine/retry.ex", "systems/engine.md"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	labels := issueLabels(t, tr, cfg, i.ID)
	if labels["system:delivery"] {
		t.Error("system:delivery was not released — it holds the mutex against every other delivery ticket for as long as it stays")
	}
	if !labels["system:engine"] {
		t.Error("system:engine is what the pass declared and must survive")
	}
	// Every other label on the ticket belongs to somebody else.
	if !labels["harness"] {
		t.Error("a non-mutex label was released — design has no business with it")
	}
}

// Design's touch list is a prediction; the branch is evidence. CI
// derives the labels it demands from the diff, so releasing one the diff
// still needs fails the next push — and the narrowing pass cannot know
// what an earlier pass or a dev round already put on the branch.
func TestALabelTheBranchStillNeedsIsKeptAndSaidSo(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeDoc(t, cfg, "systems", "delivery", "lib/delivery/**")
	writeDoc(t, cfg, "systems", "engine", "lib/engine/**")
	i := seed(t, tr, cfg, "Retry policy", "The argument.", protocol.ReadyForDesign)
	for _, l := range []string{"system:delivery", "system:engine"} {
		if err := p.AddTicketLabel(ctx, i.ID, l); err != nil {
			t.Fatal(err)
		}
	}
	res, err := ClaimDesign(ctx, p, i.Key, "run_59", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Systems: []string{"engine"}, Summary: "Engine only."}
	// An earlier round already wrote a delivery file onto the branch.
	branch := []string{"lib/engine/retry.ex", "lib/delivery/queue.ex"}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), branch, nil, ""); err != nil {
		t.Fatal(err)
	}
	if !issueLabels(t, tr, cfg, i.ID)["system:delivery"] {
		t.Error("released a label the branch's own diff requires — the next push fails the audit on it")
	}
	if !commentContains(t, tr, cfg, i.ID, "Kept `system:delivery`") {
		t.Error("kept it silently: the pass and the branch disagree about this ticket's scope, which is a thing for a person to look at")
	}
}

// A wiring gap must not read as permission. resolveDiscoveredLabel gives
// its own diff the same reading, and for the same reason.
func TestNothingIsReleasedWithoutABranchFileList(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeDoc(t, cfg, "systems", "delivery", "lib/delivery/**")
	writeDoc(t, cfg, "systems", "engine", "lib/engine/**")
	i := seed(t, tr, cfg, "Retry policy", "The argument.", protocol.ReadyForDesign)
	if err := p.AddTicketLabel(ctx, i.ID, "system:delivery"); err != nil {
		t.Fatal(err)
	}
	res, err := ClaimDesign(ctx, p, i.Key, "run_61", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Systems: []string{"engine"}, Summary: "Engine only."}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if !issueLabels(t, tr, cfg, i.ID)["system:delivery"] {
		t.Error("released a label with nothing to check it against")
	}
	if !commentContains(t, tr, cfg, i.ID, "No branch-file list reached the finish step") {
		t.Error("left the label in place without saying so — nobody knows the mutex is still held")
	}
}

func issueLabels(t *testing.T, tr *tracker.Memory, cfg *config.Config, id string) map[string]bool {
	t.Helper()
	issues, err := tr.ListIssues(context.Background(), cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range issues {
		if i.ID == id {
			out := map[string]bool{}
			for _, l := range i.Labels {
				out[l] = true
			}
			return out
		}
	}
	t.Fatalf("no issue %s", id)
	return nil
}

func commentContains(t *testing.T, tr *tracker.Memory, cfg *config.Config, id, want string) bool {
	t.Helper()
	issues, err := tr.ListIssues(context.Background(), cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range issues {
		if i.ID != id {
			continue
		}
		for _, c := range i.Comments {
			if strings.Contains(c.Body, want) {
				return true
			}
		}
	}
	return false
}

// The record review's four answers at finish, each one visible on the
// ticket rather than implied (DESIGN §4).

func TestLoadRecordReviewValidation(t *testing.T) {
	write := func(body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "review.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct{ name, body, wantErr string }{
		{"pass", `{"verdict":"pass"}`, ""},
		{"pass with callouts", `{"verdict":"pass","findings":[{"file":"systems/a.md","quote":"The first draft","why":"a draft"}]}`, ""},
		{"decline", `{"verdict":"decline","findings":[{"file":"systems/a.md","quote":"Design review threw","why":"a review round"}]}`, ""},
		{"unknown verdict", `{"verdict":"maybe"}`, "not one of pass, decline"},
		// A decline with nothing to point at is a rejection the next
		// pass repeats (DESIGN §3), so it is refused here rather than
		// posted as an empty comment.
		{"decline with no findings", `{"verdict":"decline"}`, "no findings"},
		{"finding with no quote", `{"verdict":"decline","findings":[{"file":"systems/a.md","why":"x"}]}`, "names no file or no passage"},
		{"contradiction", `{"verdict":"decline","findings":[{"file":"systems/a.md","quote":"One Repo","why":"the entry says stores never share","kind":"contradiction","id":"foundation#17"}]}`, ""},
		{"prefixed contradiction", `{"verdict":"decline","findings":[{"file":"screens/board.md","quote":"Two tabs","why":"x","kind":"contradiction","id":"screen:board#2"}]}`, ""},
		{"ticket-minted contradiction", `{"verdict":"decline","findings":[{"file":"systems/a.md","quote":"One Repo","why":"x","kind":"contradiction","id":"generation#ORC-247-2"}]}`, ""},
		// A ticket reference is not an id: the counter is what names a rule.
		{"contradiction naming a ticket, not a rule", `{"verdict":"decline","findings":[{"file":"systems/a.md","quote":"One Repo","why":"x","kind":"contradiction","id":"generation#ORC-247"}]}`, "names no rule"},
		// The writer has to find the entry, and the rule's wording is
		// exactly what changed, so the id is the only handle.
		{"contradiction with no id", `{"verdict":"decline","findings":[{"file":"systems/a.md","quote":"One Repo","why":"x","kind":"contradiction"}]}`, "names no rule"},
		{"contradiction with a bare id", `{"verdict":"decline","findings":[{"file":"systems/a.md","quote":"One Repo","why":"x","kind":"contradiction","id":"#17"}]}`, "names no rule"},
		{"unknown kind", `{"verdict":"decline","findings":[{"file":"systems/a.md","quote":"One Repo","why":"x","kind":"style"}]}`, "not one of narration, contradiction"},
	} {
		_, err := LoadRecordReview(write(c.body))
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: want error containing %q, got %v", c.name, c.wantErr, err)
		}
	}
	if _, err := LoadRecordReview(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("an absent review file loaded as something; the action decides whether one was owed, and this must not read silence as a pass")
	}
}

func TestARecordReviewDeclineSendsTheTicketBackWithItsFindings(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_70", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}, Summary: "Two states; the cap copy is a decision."}
	review := &RecordReview{Verdict: "decline", Findings: []RecordFinding{
		{File: "systems/caps.md", Quote: "Design review threw the first draft back", Why: "a review round, not a rule"},
	}}
	// A preview URL is handed in and must not be posted: there is nothing
	// to review yet, and a link on a ticket going back to the queue is a
	// link to work the author was not asked to look at.
	if err := FinishDesign(ctx, p, h, res, o, "https://preview.example", "", designedSpec(t, cfg, res.TicketKey), nil, review, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.ReadyForRedesign {
		t.Errorf("state = %q, want ready_for_redesign: a decline takes the author's own decline route, into the queue whose name says so (DESIGN §3)", got)
	}
	if len(h.PRs) != 0 {
		t.Errorf("a declined pass opened a PR: %+v", h.PRs)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	summaryAt, declineAt, previewAt := -1, -1, -1
	for n, c := range issues[0].Comments {
		if strings.Contains(c.Body, o.Summary) {
			summaryAt = n
		}
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok {
			continue
		}
		switch m.Kind {
		case marker.RecordReview:
			declineAt = n
			if m.Fields["verdict"] != "decline" {
				t.Errorf("marker verdict = %q", m.Fields["verdict"])
			}
			for _, want := range []string{"systems/caps.md", "Design review threw the first draft back", "a review round, not a rule"} {
				if !strings.Contains(c.Body, want) {
					t.Errorf("the decline's prose does not carry %q — the next pass has to be able to find the passage", want)
				}
			}
			if prose, worth := marker.Prose(c.Body); !worth || prose == "" {
				t.Error("the decline's prose is dropped as bookkeeping; the next design pass would be told it was sent back and never why")
			}
		case marker.Preview:
			previewAt = n
		}
	}
	if declineAt < 0 {
		t.Fatal("no record-review marker on a declined ticket")
	}
	if summaryAt < 0 || summaryAt > declineAt {
		t.Errorf("summary at %d, decline at %d: the newest comment is the scope (DESIGN §2.3), so the findings go last", summaryAt, declineAt)
	}
	if previewAt >= 0 {
		t.Error("a preview link was posted on a pass that is not going to Design review")
	}
}

func TestARecordReviewPassIsSaidOnTheTicket(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_71", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}, Summary: "Two states."}
	if err := FinishDesign(ctx, p, h, res, o, "https://preview.example", "", designedSpec(t, cfg, res.TicketKey), nil, &RecordReview{Verdict: "pass"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.DesignReview {
		t.Errorf("state = %q, want design_review", got)
	}
	if len(h.PRs) != 1 {
		t.Errorf("want one draft PR, got %+v", h.PRs)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	passAt, previewAt := -1, -1
	for n, c := range issues[0].Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok {
			continue
		}
		switch m.Kind {
		case marker.RecordReview:
			passAt = n
			if m.Fields["verdict"] != "pass" {
				t.Errorf("marker verdict = %q", m.Fields["verdict"])
			}
		case marker.Preview:
			previewAt = n
		}
	}
	// A ticket with no marker and one whose review passed look the same
	// otherwise, and only one of them was checked.
	if passAt < 0 {
		t.Fatal("a passing review left nothing on the ticket")
	}
	if previewAt < 0 || passAt > previewAt {
		t.Errorf("pass marker at %d, preview at %d: the verdict is posted before the ticket asks to be looked at", passAt, previewAt)
	}
}

func TestNoRecordReviewLeavesTheArtifactsPassUnchanged(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_72", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}, Summary: "Two states."}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.DesignReview {
		t.Errorf("state = %q, want design_review", got)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	for _, c := range issues[0].Comments {
		if m, ok, err := marker.Parse(c.Body); err == nil && ok && m.Kind == marker.RecordReview {
			t.Errorf("a pass that owed no review posted a verdict anyway: %s", c.Body)
		}
	}
}

// Fail open, never silently: the review is a proposal about the record
// and a broken reviewer must not cost a good design pass (DESIGN §4) —
// but a review that quietly did not run is a gate that looks armed and
// is not.
func TestAReviewerThatDiedIsSaidAndThePassProceeds(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_73", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}, Summary: "Two states."}
	const death = "the subscription model run exited 249\n--- stderr ---\nclaude native binary not installed"
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, nil, death); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.DesignReview {
		t.Errorf("state = %q, want design_review: a dead reviewer must not block the pass", got)
	}
	if len(h.PRs) != 1 {
		t.Errorf("want one draft PR, got %+v", h.PRs)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	said := false
	for _, c := range issues[0].Comments {
		if strings.Contains(c.Body, "did not run") && strings.Contains(c.Body, "exited 249") {
			said = true
		}
	}
	if !said {
		t.Error("the reviewer died and the ticket does not say so; a review that silently did not run reads as one that passed")
	}
}

// A decline has nothing to withhold on an outcome that never reaches
// Design review, so it is recorded rather than acted on — and never
// swallowed.
func TestADeclineOnADecisionlessPassIsRecordedNotActedOn(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Rename a helper", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_74", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "decisionless", Systems: []string{"caps"}, Summary: "Nothing to decide."}
	review := &RecordReview{Verdict: "decline", Findings: []RecordFinding{{File: "docs/non-goals.md", Quote: "The second pass considered", Why: "pass narration"}}}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, review, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.ReadyForDev {
		t.Errorf("state = %q, want ready_for_dev: the decisionless outcome stands", got)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	found := false
	for _, c := range issues[0].Comments {
		if m, ok, err := marker.Parse(c.Body); err == nil && ok && m.Kind == marker.RecordReview {
			found = true
			if !strings.Contains(c.Body, "not acted on") || !strings.Contains(c.Body, "The second pass considered") {
				t.Errorf("the recorded decline does not say it was not acted on, or lost its finding: %s", c.Body)
			}
		}
	}
	if !found {
		t.Error("a decline on a decisionless pass vanished")
	}
}

// Every finding was narration before the kind existed, so an absent kind
// reads as narration rather than as an error a working reviewer would
// suddenly start producing.
func TestAFindingWithNoKindIsNarration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(p, []byte(`{"verdict":"decline","findings":[{"file":"systems/a.md","quote":"Design review threw","why":"a round"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRecordReview(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Findings[0].Kind != "narration" {
		t.Errorf("Kind = %q, want narration", r.Findings[0].Kind)
	}
}

// A contradiction is a decline like narration — same route, same
// second-decline parking — and the ticket carries the id, because the
// next pass's first move is `pipeline reasons` on it.
func TestAContradictionDeclineNamesTheRuleOnTheTicket(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_71", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}, Summary: "The cap copy is a decision."}
	review := &RecordReview{Verdict: "decline", Findings: []RecordFinding{
		{File: "systems/caps.md", Quote: "Stores may share a connection", Why: "the entry records why they never do", Kind: "contradiction", ID: "caps#17"},
	}}
	if err := FinishDesign(ctx, p, h, res, o, "", "", designedSpec(t, cfg, res.TicketKey), nil, review, ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.ReadyForRedesign {
		t.Errorf("state = %q, want ready_for_redesign", got)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	found := false
	for _, c := range issues[0].Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok || m.Kind != marker.RecordReview {
			continue
		}
		found = true
		for _, want := range []string{"contradicts `caps#17`", "Stores may share a connection", "pipeline reasons"} {
			if !strings.Contains(c.Body, want) {
				t.Errorf("the decline's prose does not carry %q:\n%s", want, c.Body)
			}
		}
	}
	if !found {
		t.Fatal("no record-review marker")
	}
}

// The redesign queue is claimed exactly as the fresh one is: a normal
// pass, whose scope is the description and whose newest comment names
// what the last pass got wrong. Reading it as a re-read would hand the
// agent the wrong instructions for the wrong state.
func TestAClaimFromTheRedesignQueueIsANormalPass(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForRedesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_95", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "design" {
		t.Errorf("mode = %q, want a normal pass", res.Mode)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Designing {
		t.Errorf("state = %q, want the claim to have taken it out of the redesign queue", got)
	}
}

// designedSpec writes the spec file a design pass owes and returns it as
// that pass's changed-file list. FinishDesign holds an artifacts pass to
// both halves — that CHANGE.md names this ticket, and that this pass
// wrote it — so a fixture that skips it stands for a pass the protocol
// does not allow.
func designedSpec(t *testing.T, cfg *config.Config, key string) []string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cfg.Root, changespec.Name),
		[]byte(changespec.Header(key, "the sketch")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{changespec.Name}
}

// Both outcomes that commit are held to the sketch, and neither was
// asserted until a probe removed the check and the suite stayed green —
// the fixtures hand every pass a spec, so nothing exercised the refusal.
func TestADesignPassThatCommitsNoSpecIsRefused(t *testing.T) {
	for _, outcome := range []string{"artifacts", "decisionless"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			tr, h, cfg, p := world(t)
			i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)
			res, err := ClaimDesign(ctx, p, i.Key, "run_1", "u", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			o := &DesignOutcome{Outcome: outcome, Systems: []string{"caps"}, Summary: "s"}
			if outcome == "artifacts" {
				o.Screens = []string{"cap"}
			}
			// The pass changed something and it was not the sketch.
			err = FinishDesign(ctx, p, h, res, o, "", "", []string{"systems/caps.md"}, nil, nil, "")
			if err == nil {
				t.Fatalf("%s: a pass with no %s was accepted", outcome, changespec.Name)
			}
			if !strings.Contains(err.Error(), changespec.Name) {
				t.Errorf("%s: the refusal does not name the file: %v", outcome, err)
			}
		})
	}
}

// The other half: the file is there and names this ticket, but this pass
// left the previous one's copy in place. It is the failure the first check
// cannot see, because the file is at the root of every branch and always
// names somebody.
func TestADesignPassThatDidNotRewriteTheSpecIsRefused(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.ReadyForDesign)
	res, err := ClaimDesign(ctx, p, i.Key, "run_1", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	designedSpec(t, cfg, res.TicketKey) // present, and correctly named
	o := &DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}, Systems: []string{"caps"}, Summary: "s"}
	err = FinishDesign(ctx, p, h, res, o, "", "", []string{"screens/cap.md"}, nil, nil, "")
	if err == nil {
		t.Fatal("a pass that did not rewrite the spec was accepted")
	}
	if !strings.Contains(err.Error(), "did not write it") {
		t.Errorf("the refusal does not say the pass failed to write it: %v", err)
	}
}
