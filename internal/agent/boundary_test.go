package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/retro"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

func TestBoundaryFullPass(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	now := time.Now()

	tr.AddMilestone(cfg.Tracker.ProjectID, "M: alpha", 1)
	done := seed(t, tr, cfg, "Shipped thing", "d", protocol.Done)
	if err := tr.Mutate(done.ID, func(i *tracker.Issue) { i.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}
	debt := seed(t, tr, cfg, "Old debt", "d", protocol.Backlog)

	// The boundary ticket, as the sweep would have created it, moved to
	// In progress by the author (the signal).
	boundary, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Milestone boundary — M: alpha", Description: "machinery",
		StateID: stateID(t, tr, cfg, protocol.InProgress),
		Labels:  []string{"milestone-boundary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Mutate(boundary.ID, func(i *tracker.Issue) { i.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}

	plan, err := ClaimBoundary(ctx, p, boundary.Key, "run_60", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Milestone != "M: alpha" || len(plan.Done) != 0 {
		t.Fatalf("plan = %+v", plan)
	}

	if err := BoundaryArchive(ctx, p, h, plan, now); err != nil {
		t.Fatal(err)
	}
	if !tr.Archived(done.ID) {
		t.Error("Done issue not archived")
	}
	if tr.Archived(boundary.ID) {
		t.Error("the boundary ticket must never archive itself — it is the record of the pass")
	}
	retro, ok := h.Files["docs/retros/m-alpha.md"]
	if !ok || !strings.Contains(retro, done.Key) {
		t.Errorf("retro note missing or incomplete: %q", retro)
	}

	ps := &Proposals{
		Proposals: []Proposal{
			{Title: "Extract cap module", Description: "gating", Kind: "debt", Gating: true, Dedupe: "M: alpha/extract-cap"},
			{Title: "Cap screen empty state", Description: "finding", Kind: "design", Dedupe: "M: alpha/cap-empty"},
		},
		Ranking: []RankEntry{{Key: debt.Key, Priority: 2}},
	}
	if err := BoundaryFile(ctx, p, plan, ps, now); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, boundary.ID); got != protocol.BoundaryReview {
		t.Errorf("state = %q, want boundary_review", got)
	}

	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	proposals := 0
	for _, i := range issues {
		if strings.Contains(i.Description, "[pipeline:v1:triage-proposal]") {
			proposals++
		}
		if i.ID == debt.ID && i.Priority != 2 {
			t.Errorf("ranking not applied: priority = %d", i.Priority)
		}
	}
	if proposals != 2 {
		t.Errorf("proposals filed = %d, want 2", proposals)
	}
}

// The dangerous re-run (DESIGN 10): a resumed boundary must not file
// proposals twice, and a resumed archive must not duplicate the retro.
func TestBoundaryResumeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	now := time.Now()

	tr.AddMilestone(cfg.Tracker.ProjectID, "M: alpha", 1)
	boundary, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Milestone boundary — M: alpha", Description: "machinery",
		StateID: stateID(t, tr, cfg, protocol.InProgress),
		Labels:  []string{"milestone-boundary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Mutate(boundary.ID, func(i *tracker.Issue) { i.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}

	plan, err := ClaimBoundary(ctx, p, boundary.Key, "run_61", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := BoundaryArchive(ctx, p, h, plan, now); err != nil {
		t.Fatal(err)
	}
	ps := &Proposals{Proposals: []Proposal{
		{Title: "Extract cap module", Description: "x", Kind: "debt", Dedupe: "M: alpha/extract-cap"},
	}}
	if err := BoundaryFile(ctx, p, plan, ps, now); err != nil {
		t.Fatal(err)
	}

	// Crash simulation: the author routes it back (Blocked -> In progress
	// per DESIGN 10's recovery), and the whole pass re-runs from claim.
	if err := tr.UpdateIssueState(ctx, boundary.ID, stateID(t, tr, cfg, protocol.InProgress)); err != nil {
		t.Fatal(err)
	}
	plan2, err := ClaimBoundary(ctx, p, boundary.Key, "run_62", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.Done[StepArchive] || !plan2.Done[StepScan] || !plan2.Done[StepFile] {
		t.Fatalf("resume must see completed steps: %+v", plan2.Done)
	}
	if err := BoundaryArchive(ctx, p, h, plan2, now); err != nil {
		t.Fatal(err)
	}
	// Filing again with nil proposals recovers from the scan comment and
	// the dedupe keys keep it a no-op.
	if err := BoundaryFile(ctx, p, plan2, nil, now); err != nil {
		t.Fatal(err)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	proposals := 0
	for _, i := range issues {
		if strings.Contains(i.Description, "M: alpha/extract-cap") {
			proposals++
		}
	}
	if proposals != 1 {
		t.Errorf("proposal filed %d times across a resume, want exactly 1", proposals)
	}
}

// The archive step carries the merge shas out, for the same reason it
// carries the harness findings out: it is the step that destroys them.
// The rehearsal reset learns what to revert from `merged` markers on the
// tickets it archives, and a boundary archives those tickets first — so
// a reset run after one found nothing, wrote an empty merge list, said
// "the last rehearsal merged nothing" and left the commits on main.
// Measured on orchestration-dummy: ORC-23 (PR #15) survived, with
// `retro: Rehearsal 1` directly above it in the log.
func TestBoundaryArchiveRecordsWhatEachTicketMerged(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	now := time.Now()

	tr.AddMilestone(cfg.Tracker.ProjectID, "M1", 1)
	landed := seed(t, tr, cfg, "Add a farewell to the home screen", "d", protocol.Done)
	if err := tr.Mutate(landed.ID, func(i *tracker.Issue) { i.Milestone = "M1" }); err != nil {
		t.Fatal(err)
	}
	sha := "0655929c405d57bc77b341c476267e45472e3985"
	m := marker.Marker{Kind: marker.Merged, Fields: map[string]string{"sha": sha, "pr": "15"}}
	if err := tr.CommentOnIssue(ctx, landed.ID, m.Comment("Reconciled and merged.")); err != nil {
		t.Fatal(err)
	}
	// Done without landing anything, which most boundaries also archive.
	closed := seed(t, tr, cfg, "Decide the greeting copy", "d", protocol.Done)
	if err := tr.Mutate(closed.ID, func(i *tracker.Issue) { i.Milestone = "M1" }); err != nil {
		t.Fatal(err)
	}

	boundary := seedBoundary(t, tr, cfg)
	plan, err := ClaimBoundary(ctx, p, boundary.Key, "run_70", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := BoundaryArchive(ctx, p, h, plan, now); err != nil {
		t.Fatal(err)
	}

	note, ok := h.Files["docs/retros/m1.md"]
	if !ok {
		t.Fatalf("no retro note; files = %v", h.Files)
	}
	got := retro.Parse(note)
	if len(got) != 2 {
		t.Fatalf("note carries %d entries, want both archived tickets:\n%s", len(got), note)
	}
	for _, e := range got {
		switch e.Key {
		case landed.Key:
			if len(e.SHAs) != 1 || e.SHAs[0] != sha {
				t.Errorf("%s: shas = %v, want the merge the reset has to revert", e.Key, e.SHAs)
			}
		case closed.Key:
			if len(e.SHAs) != 0 {
				t.Errorf("%s never merged, but the note claims %v", e.Key, e.SHAs)
			}
		default:
			t.Errorf("unexpected entry %+v", e)
		}
	}
}

func TestParseProposalsRejectsBugs(t *testing.T) {
	_, err := ParseProposals([]byte(`{"proposals":[{"title":"Crash","kind":"bug","dedupe":"m/crash"}]}`))
	if err == nil || !strings.Contains(err.Error(), "never bugs") {
		t.Errorf("bugs must never park in Triage, got %v", err)
	}
}

// seedBoundary makes a boundary ticket the way the sweep would.
func seedBoundary(t *testing.T, tr *tracker.Memory, cfg *config.Config) tracker.Issue {
	t.Helper()
	i, err := tr.CreateIssue(context.Background(), tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Milestone boundary — M1", Description: "machinery",
		StateID: stateID(t, tr, cfg, protocol.InProgress),
		Labels:  []string{"milestone-boundary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Mutate(i.ID, func(is *tracker.Issue) { is.Milestone = "M1" }); err != nil {
		t.Fatal(err)
	}
	return i
}

// The live suite has three outcomes, not two. A test runner given a tag
// filter that matches nothing exits non-zero — an `--only` filter with
// no match is an error, not an empty pass — so a project that has not
// written its first :live test reported a FAILING live suite at every
// boundary. The first real boundary spent an investigation on exactly
// that, and a gate that is red for structural reasons is a gate the
// author learns to skip.
//
// "no-tests" is neither verdict on purpose. Reporting it as a pass would
// claim the world was checked when nothing ran, which is the failure
// mode the gate exists to prevent (DESIGN §10).
func TestLiveSuiteReportsNoTestsAsItsOwnOutcome(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	now := time.Now()

	boundary, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Milestone boundary — M: alpha", Description: "machinery",
		StateID: stateID(t, tr, cfg, protocol.InProgress),
		Labels:  []string{"milestone-boundary"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := LiveSuiteReport(ctx, p, boundary.Key, "no-tests", "https://gh/run/1", now); err != nil {
		t.Fatalf("no-tests is a legal outcome and was refused: %v", err)
	}
	if err := LiveSuiteReport(ctx, p, boundary.Key, "inconclusive", "https://gh/run/1", now); err == nil {
		t.Error("an unknown result was accepted; the marker's vocabulary is its contract")
	}

	issues, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, i := range issues {
		if i.ID != boundary.ID {
			continue
		}
		for _, c := range i.Comments {
			m, ok, err := marker.Parse(c.Body)
			if err == nil && ok && m.Kind == marker.LiveSuite {
				if got := m.Fields["result"]; got != "no-tests" {
					t.Errorf("marker result = %q, want no-tests", got)
				}
				body = c.Body
			}
		}
	}
	if body == "" {
		t.Fatal("no live-suite marker on the boundary ticket")
	}
	// The author reads this at the boundary and has to be able to tell
	// "nothing was checked" from "everything passed" without opening the
	// run.
	if !strings.Contains(body, "NO tests") || !strings.Contains(body, "nothing was checked") {
		t.Errorf("the comment does not say plainly that nothing ran:\n%s", body)
	}
	if strings.Contains(body, "FAILED") {
		t.Errorf("no-tests reads as a failure, which is the thing that trains the signal into noise:\n%s", body)
	}
}

// Harness findings live in comments on the milestone's tickets, and the
// archive step removes those tickets. A boundary that dies between
// archiving and scanning comes back to a claim that collects nothing —
// so the findings the milestone recorded vanish, with no trace that
// there ever were any. Same shape as the rehearsal reset's merge list:
// the information exists at exactly one moment, and the step that ends
// that moment owns preserving it.
func TestFindingsSurviveTheArchiveOnAResumedBoundary(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	now := time.Now()

	tr.AddMilestone(cfg.Tracker.ProjectID, "M: alpha", 1)
	done := seed(t, tr, cfg, "Shipped thing", "d", protocol.Done)
	if err := tr.Mutate(done.ID, func(i *tracker.Issue) { i.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}
	// The finding, recorded by the run that hit it, on a ticket this
	// boundary is about to archive.
	if err := PostHarnessFindings(ctx, p, done.ID, []HarnessFinding{{
		Title:  "Dev runner has no Postgres",
		Detail: "mix test is a configured gate and the runner cannot satisfy it.",
		Dedupe: "dev-runner-no-postgres",
	}}); err != nil {
		t.Fatal(err)
	}

	boundary, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Milestone boundary — M: alpha", Description: "machinery",
		StateID: stateID(t, tr, cfg, protocol.InProgress),
		Labels:  []string{"milestone-boundary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Mutate(boundary.ID, func(i *tracker.Issue) { i.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}

	plan, err := ClaimBoundary(ctx, p, boundary.Key, "run_1", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.HarnessFindings) != 1 {
		t.Fatalf("the first claim collected %d findings, want 1", len(plan.HarnessFindings))
	}
	if err := BoundaryArchive(ctx, p, h, plan, now); err != nil {
		t.Fatal(err)
	}

	// The run dies here. The author moves the ticket back to In progress
	// and it is dispatched again — a fresh claim, against a world where
	// the ticket carrying the finding no longer appears in any listing.
	resumed, err := ClaimBoundary(ctx, p, boundary.Key, "run_2", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Done[StepArchive] {
		t.Fatal("the resumed plan does not see the archive step as done; this test is not exercising a resume")
	}
	if len(resumed.HarnessFindings) != 1 {
		t.Fatalf("the resumed claim has %d findings, want 1 — the milestone's findings died with the archive", len(resumed.HarnessFindings))
	}
	got := resumed.HarnessFindings[0]
	if got.Dedupe != "dev-runner-no-postgres" || !strings.Contains(got.Detail, "configured gate") {
		t.Errorf("the recovered finding lost its content: %+v", got)
	}
}

// The key is derived from the subject, because the model's own key is
// its phrasing for one scan and phrasing is a choice. Two scans of one
// tree wrote two keys for one finding on ORC-45 and both filed: four
// tickets, two findings.
func TestTheDedupeKeyComesFromTheSubjectNotThePhrasing(t *testing.T) {
	first := Proposal{
		Title:   "Arm the six gate lines the repo declares and CI does not run",
		Dedupe:  "tech-debt-before-the-engine/declared-gate-set-not-armed-in-ci",
		Subject: "ci.yml",
	}
	second := Proposal{
		Title:   "Arm the gates that are green locally and armed nowhere",
		Dedupe:  "tech-debt-before-the-engine/arm-the-unarmed-gate-set",
		Subject: "ci.yml",
	}
	a := dedupeKey("Tech debt · before the engine", first)
	b := dedupeKey("Tech debt · before the engine", second)
	if a != b {
		t.Errorf("two scans of one subject produced two keys:\n  %s\n  %s", a, b)
	}
	if a == first.Dedupe {
		t.Error("the key is still the model's own phrasing")
	}
}

// A scan that named no subject keeps its own key, so a resume replaying
// a recorded scan from before this change still dedupes against what
// that scan filed.
func TestAProposalWithNoSubjectKeepsItsOwnKey(t *testing.T) {
	p := Proposal{Title: "Something", Dedupe: "milestone/finding"}
	if got := dedupeKey("Milestone", p); got != "milestone/finding" {
		t.Errorf("key = %q, want the model's own", got)
	}
	if got := dedupeKey("Milestone", Proposal{Dedupe: "k", Subject: "   "}); got != "k" {
		t.Errorf("a blank subject was treated as a subject: %q", got)
	}
}

// Different subjects stay different, which is the half that matters for
// not swallowing real work.
func TestDifferentSubjectsKeepDifferentKeys(t *testing.T) {
	a := dedupeKey("M", Proposal{Subject: "ci.yml", Dedupe: "x"})
	b := dedupeKey("M", Proposal{Subject: "pipeline.config.json", Dedupe: "x"})
	if a == b {
		t.Errorf("two subjects collapsed to one key: %s", a)
	}
}

// The prompt's schema and the parser have to name the same kinds.
//
// They drifted: the schema said `"kind": "debt" | "design"` while the
// harness-findings section four paragraphs up said to file those with
// `"kind": "harness"`, which ParseProposals accepts. A boundary reading
// only the schema files a pipeline problem as product debt or drops it,
// and the one channel for "the harness is broken" quietly empties.
//
// Asserted against the prompt file rather than restated here, because a
// second copy of the list is the thing that drifted in the first place.
func TestBoundaryPromptSchemaNamesEveryKindTheParserAccepts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "prompts", "boundary.md"))
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, `"kind":`) && strings.Contains(line, "|") {
			schema = line
		}
	}
	if schema == "" {
		t.Fatal("no kind line in the prompt's output schema — it moved or was dropped")
	}

	// Every kind the schema offers must parse.
	quoted := regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(schema, -1)
	offered := map[string]bool{}
	for _, m := range quoted {
		if m[1] == "kind" || m[1] == "gating" {
			continue
		}
		offered[m[1]] = true
		body := fmt.Sprintf(`{"proposals":[{"title":"t","kind":%q,"dedupe":"m/d"}]}`, m[1])
		if _, err := ParseProposals([]byte(body)); err != nil {
			t.Errorf("the schema offers kind %q and the parser rejects it: %v", m[1], err)
		}
	}
	// And every kind the parser accepts must be offered, or a boundary
	// reading the schema never learns the channel exists.
	for _, kind := range []string{"debt", "design", "harness"} {
		if !offered[kind] {
			t.Errorf("the parser accepts kind %q and the schema never names it: %s", kind, strings.TrimSpace(schema))
		}
	}
}

// The grooming pass is asked to re-rank the debt backlog (DESIGN §10)
// and could not see one. The claim carried the milestone roster — names
// and open counts — and nothing else, so the pass reordered an invisible
// list and returned an empty ranking, twice in a row. On the ticket that
// is indistinguishable from a pass that read the order and approved it.
func TestClaimBoundaryCarriesTheDebtBacklog(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	now := time.Now()
	tr.AddMilestone(cfg.Tracker.ProjectID, "M1", 1)

	debt := func(title string, priority int, gating bool) tracker.Issue {
		i := seed(t, tr, cfg, title, "d", protocol.Backlog)
		if err := tr.AddIssueLabel(ctx, cfg.Tracker.TeamID, i.ID, "tech-debt"); err != nil {
			t.Fatal(err)
		}
		if err := tr.Mutate(i.ID, func(is *tracker.Issue) {
			is.Priority = priority
			is.Description = fmt.Sprintf("[pipeline:v1:triage-proposal] dedupe=m/%s gating=%t\n\nx", title, gating)
		}); err != nil {
			t.Fatal(err)
		}
		return i
	}
	low := debt("low", 4, false)
	high := debt("high", 2, false)
	gate := debt("gate", 4, true)
	// Not debt, and already scheduled debt: neither is backlog.
	seed(t, tr, cfg, "A feature", "d", protocol.Backlog)
	scheduled := debt("scheduled", 1, false)
	if err := tr.Mutate(scheduled.ID, func(is *tracker.Issue) { is.Milestone = "M1" }); err != nil {
		t.Fatal(err)
	}

	boundary := seedBoundary(t, tr, cfg)
	plan, err := ClaimBoundary(ctx, p, boundary.Key, "run_80", "u", now)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, c := range plan.Backlog {
		got = append(got, c.Key)
	}
	// Gating first, then by priority. The scheduled one is committed
	// scope, and the feature is not debt.
	want := []string{gate.Key, high.Key, low.Key}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("backlog = %v, want %v", got, want)
	}
	for _, c := range plan.Backlog {
		if c.Key == gate.Key && !c.Gating {
			t.Error("gating read from the triage marker was lost")
		}
		if c.Key == high.Key && c.Priority != 2 {
			t.Errorf("priority = %d, want the current one — a ranking is a diff against it", c.Priority)
		}
	}
}
