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

	// A completed pass ends the resume window, so re-entry is a new pass
	// with nothing done — archive, scan and file all run again over
	// whatever has landed since (DESIGN 10).
	if err := tr.UpdateIssueState(ctx, boundary.ID, stateID(t, tr, cfg, protocol.InProgress)); err != nil {
		t.Fatal(err)
	}
	plan2, err := ClaimBoundary(ctx, p, boundary.Key, "run_62", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Done[StepArchive] || plan2.Done[StepScan] || plan2.Done[StepFile] {
		t.Fatalf("re-entry after a completed pass resumed it instead of starting a new one: %+v", plan2.Done)
	}
	if err := BoundaryArchive(ctx, p, h, plan2, now); err != nil {
		t.Fatal(err)
	}
	// The new pass scans afresh, proposes the same finding again, and
	// files it again. **This is the accepted cost of removing the dedupe
	// key**, not an oversight: no automatic key could be made
	// deterministic (see TestTwoProposalsOnOneSubjectBothFile), and a
	// duplicate the author declines at Boundary review is cheaper than a
	// finding silently dropped.
	//
	// What is still idempotent is the resume *within* a pass: BoundaryFile
	// is guarded by plan.Done[StepFile], and stepDone writes that marker
	// even when individual proposals failed. Re-entry after a completed
	// pass is a different thing — a new pass, by design.
	if err := BoundaryFile(ctx, p, plan2, ps, now); err != nil {
		t.Fatal(err)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	proposals := 0
	for _, i := range issues {
		if i.Title == "Extract cap module" {
			proposals++
		}
	}
	if proposals != 2 {
		t.Errorf("proposal filed %d times across two passes, want 2 — one per pass, deduplicated by hand", proposals)
	}
}

// The other half of the same rule: a pass that died partway is resumed,
// not restarted. The discriminator is the close marker, which only a
// pass that got all the way to the hand-back posts.
func TestBoundaryResumesAPassThatDiedPartway(t *testing.T) {
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

	plan, err := ClaimBoundary(ctx, p, boundary.Key, "run_71", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := BoundaryArchive(ctx, p, h, plan, now); err != nil {
		t.Fatal(err)
	}
	// Died here — archive landed, the model step never ran.
	plan2, err := ClaimBoundary(ctx, p, boundary.Key, "run_72", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.Done[StepArchive] {
		t.Error("a resumed pass re-runs the archive it already did")
	}
	if plan2.Done[StepScan] || plan2.Done[StepFile] {
		t.Errorf("a resumed pass skipped steps it never ran: %+v", plan2.Done)
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

// A second pass over a milestone archives the tickets that landed since
// the first, and the retro note has to grow to hold them. It used to
// refuse: the note was written with PutFileIfAbsent, so the second
// pass's tickets were archived with nothing recording their keys or
// their merge shas — invisible to the next duplicate check and missing
// from the rehearsal reset's revert list, which is the one job the note
// has.
func TestASecondArchivePassAddsToTheRetroNoteRatherThanSkippingIt(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	now := time.Now()

	tr.AddMilestone(cfg.Tracker.ProjectID, "M1", 1)
	boundary := seedBoundary(t, tr, cfg)

	archivePass := func(title, sha, run string) {
		t.Helper()
		i := seed(t, tr, cfg, title, "d", protocol.Done)
		if err := tr.Mutate(i.ID, func(is *tracker.Issue) { is.Milestone = "M1" }); err != nil {
			t.Fatal(err)
		}
		m := marker.Marker{Kind: marker.Merged, Fields: map[string]string{"sha": sha, "pr": "1"}}
		if err := tr.CommentOnIssue(ctx, i.ID, m.Comment("Reconciled and merged.")); err != nil {
			t.Fatal(err)
		}
		plan, err := ClaimBoundary(ctx, p, boundary.Key, run, "u", now)
		if err != nil {
			t.Fatal(err)
		}
		// Each call is its own pass: the previous one's archive marker
		// must not skip this one.
		plan.Done = map[string]bool{}
		if err := BoundaryArchive(ctx, p, h, plan, now); err != nil {
			t.Fatal(err)
		}
	}
	first := "0655929c405d57bc77b341c476267e45472e3985"
	second := "aff0c45ba1c9d0f2e3b4a5968778695a4b3c2d1e"
	archivePass("Add a farewell", first, "run_80")
	archivePass("Pay down the greeting debt", second, "run_81")

	got := retro.Parse(h.Files["docs/retros/m1.md"])
	if len(got) != 2 {
		t.Fatalf("note carries %d entries, want both passes' tickets:\n%s", len(got), h.Files["docs/retros/m1.md"])
	}
	shas := map[string]bool{}
	for _, e := range got {
		for _, s := range e.SHAs {
			shas[s] = true
		}
	}
	for _, want := range []string{first, second} {
		if !shas[want] {
			t.Errorf("the note lost %s, so a rehearsal reset would leave it on main:\n%s", want, h.Files["docs/retros/m1.md"])
		}
	}
}

// The reversal (DESIGN §10). Refusing the kind never stopped the
// boundary finding defects — it stopped it naming them, and a defect it
// could not name it filed as debt, which is what the composition rule
// schedules into the next debt milestone.
func TestParseProposalsAcceptsEveryKindItFiles(t *testing.T) {
	for _, kind := range []string{"debt", "design", "harness", "bug"} {
		body := `{"proposals":[{"title":"T","kind":"` + kind + `","dedupe":"m/t"}]}`
		if _, err := ParseProposals([]byte(body)); err != nil {
			t.Errorf("kind %q: %v", kind, err)
		}
	}
}

// And the vocabulary stays closed, because the filer maps kind to a
// label and an unmapped kind would file silently under tech-debt.
func TestParseProposalsRejectsAKindItCannotFile(t *testing.T) {
	_, err := ParseProposals([]byte(`{"proposals":[{"title":"T","kind":"chore","dedupe":"m/t"}]}`))
	if err == nil || !strings.Contains(err.Error(), "chore") {
		t.Errorf("want the unknown kind named back, got %v", err)
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

// Two proposals naming one subject both file. This is the behaviour the
// subject-derived dedupe key removed, and removing that key is what put
// it back.
//
// Measured on Catapult's ORC-118: seventeen proposals, sixteen filed.
// `dashboard-ci-sobelow-config-https-ignore-stale` and
// `boundary-compile-cache-hides-preexisting-violations` both named
// `.github/workflows/ci.yml` — a stale sobelow ignore and a compile-cache
// gap, sharing nothing but a filename — and the second was dropped while
// a decline note on the same ticket told the author it had been filed.
func TestTwoProposalsOnOneSubjectBothFile(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)

	for _, title := range []string{
		"ci.yml's sobelow ignore rationale is stale",
		"mix compile only re-checks boundary compliance when boundary config changes",
	} {
		if _, err := p.FileTriageProposal(ctx, title, "why", "debt", ".github/workflows/ci.yml", false); err != nil {
			t.Fatal(err)
		}
	}

	issues, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	filed := 0
	for _, i := range issues {
		if strings.Contains(i.Description, "[pipeline:v1:triage-proposal]") {
			filed++
		}
	}
	if filed != 2 {
		t.Errorf("filed %d proposals on one subject, want 2 — a dedupe rule is swallowing findings again", filed)
	}
}

// The subject rides on the ticket, because hand-deduplication is the
// mechanism now and it is what a human sorts two similar tickets by.
func TestAFiledProposalCarriesItsSubject(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)

	if _, err := p.FileTriageProposal(ctx, "Something", "why", "debt", "lib/cap.ex", false); err != nil {
		t.Fatal(err)
	}
	issues, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, i := range issues {
		if strings.Contains(i.Description, `subject="lib/cap.ex"`) || strings.Contains(i.Description, "subject=lib/cap.ex") {
			found = true
		}
	}
	if !found {
		t.Error("no subject on the filed proposal — nothing to deduplicate by hand against")
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
	//
	// Read from protocol, not restated. This loop used to hold its own
	// `{"debt", "design", "harness"}` — a fourth copy of the vocabulary,
	// in the test whose own comment says not to make one — and it was
	// not updated when `bug` was added. Dropping `bug` from the schema,
	// which would mean no boundary pass ever files one, left this green.
	for _, kind := range protocol.ProposalKinds {
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

// The two kinds have to survive the round trip through the marker, or
// the boundary sees every finding as a pipeline problem — which is the
// laundering the field exists to stop.
func TestFindingKindSurvivesTheMarker(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "arg", protocol.InProgress)

	if err := PostHarnessFindings(ctx, p, i.ID, []HarnessFinding{
		{Title: "Preflight passes without checking", Detail: "d1", Dedupe: "k-harness"},
		{Title: "A doc's worked example the loader rejects", Detail: "d2", Dedupe: "k-project", Kind: KindProject},
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]HarnessFinding{}
	for _, f := range CollectHarnessFindings(snap.Tickets) {
		byKey[f.Dedupe] = f
	}
	if len(byKey) != 2 {
		t.Fatalf("collected %d findings", len(byKey))
	}
	if byKey["k-harness"].IsProject() {
		t.Error("a harness finding came back as a project finding")
	}
	if !byKey["k-project"].IsProject() {
		t.Errorf("the project kind was lost in the marker: %+v", byKey["k-project"])
	}
	// The prose says which it is too — the author reads the ticket, not
	// the marker.
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	var bodies string
	for _, c := range issues[0].Comments {
		bodies += c.Body
	}
	if !strings.Contains(bodies, "**Project finding:") || !strings.Contains(bodies, "**Harness finding:") {
		t.Errorf("the comments do not distinguish the two kinds:\n%s", bodies)
	}
}

// Absent reads as harness. Every finding recorded before the field
// existed has no kind, and reading those as project findings would move
// a milestone's worth of pipeline problems into the debt backlog on the
// first run after an upgrade.
func TestFindingWithoutAKindIsHarness(t *testing.T) {
	if (HarnessFinding{Dedupe: "k"}).IsProject() {
		t.Error("a finding with no kind read as a project finding")
	}
}

// Every carried finding leaves the pass adjudicated. ORC-90 carried
// twelve and neither filed nor declined any of them, and nothing said
// so — they were on tickets the archive step had already deleted.
func TestUnadjudicatedNamesWhatTheScanPassedOver(t *testing.T) {
	carried := []HarnessFinding{
		{Dedupe: "k-filed", Title: "filed"},
		{Dedupe: "k-declined", Title: "declined"},
		{Dedupe: "k-dropped", Title: "dropped on the floor"},
	}
	ps := &Proposals{
		Proposals: []Proposal{{Dedupe: "k-filed"}},
		Declined:  []Decline{{Dedupe: "k-declined", Why: "fixed last week"}},
	}
	got := unadjudicated(carried, ps)
	if len(got) != 1 || !strings.Contains(got[0], "k-dropped") {
		t.Errorf("unadjudicated = %v, want only the dropped one", got)
	}
	// And a pass that adjudicated everything reports nothing, or the
	// warning becomes noise the author learns to skip.
	ps.Declined = append(ps.Declined, Decline{Dedupe: "k-dropped", Why: "not worth a ticket"})
	if got := unadjudicated(carried, ps); len(got) != 0 {
		t.Errorf("a fully adjudicated scan still reported %v", got)
	}
}

// A decline with no reason is the silent drop it replaces, one field
// wider.
func TestProposalsRefuseADeclineWithoutAReason(t *testing.T) {
	if _, err := ParseProposals([]byte(`{"declined":[{"dedupe":"k"}]}`)); err == nil {
		t.Error("a decline with no reason was accepted")
	}
	if _, err := ParseProposals([]byte(`{"declined":[{"why":"because"}]}`)); err == nil {
		t.Error("a decline naming no finding was accepted")
	}
	if _, err := ParseProposals([]byte(`{"declined":[{"dedupe":"k","why":"already fixed"}]}`)); err != nil {
		t.Errorf("a well-formed decline was refused: %v", err)
	}
}

// A filed bug must not turn up in the debt backlog, because the backlog
// is what the composition schedules into the next debt milestone — one
// milestone in two, against a floor of five. Waiting for that is the
// rescheduling the old never-bugs rule was written to prevent, and it is
// what filing a defect as debt produced (DESIGN §10).
//
// The mapping in FileTriageProposal is the whole mechanism: `tech-debt`
// is what DebtBacklog draws, so a kind that falls through to the default
// label is a kind that gets scheduled.
func TestAFiledBugStaysOutOfTheDebtBacklog(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	tr.AddMilestone(cfg.Tracker.ProjectID, "M1", 1)

	if _, err := p.FileTriageProposal(ctx, "Extract the cap module", "why", "debt", "lib/cap.ex", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.FileTriageProposal(ctx, "Preflight passes without comparing the label sets", "why", "bug", "cmd/pipeline/preflight.go", false); err != nil {
		t.Fatal(err)
	}

	boundary := seedBoundary(t, tr, cfg)
	plan, err := ClaimBoundary(ctx, p, boundary.Key, "run_90", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range plan.Backlog {
		got = append(got, c.Title)
	}
	if len(got) != 1 || got[0] != "Extract the cap module" {
		t.Errorf("backlog = %v, want the debt proposal alone", got)
	}
}

// And the kind the pass writes has to reach the label the rest of the
// pipeline reads. Checked as a set rather than one case, because the
// filer's switch has a default and a kind that falls through it files
// silently under the wrong one.
func TestProposalKindsReachTheirLabels(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	want := map[string]string{
		"debt":    "tech-debt",
		"design":  "design-inbox",
		"harness": "harness",
		"bug":     "bug",
	}
	for kind, label := range want {
		title := "proposal of kind " + kind
		if _, err := p.FileTriageProposal(ctx, title, "why", kind, "lib/x.ex", false); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		issues, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, i := range issues {
			if i.Title == title {
				got = i.Labels
			}
		}
		found := false
		for _, l := range got {
			if l == label {
				found = true
			}
		}
		if !found {
			t.Errorf("kind %q filed with labels %v, want %q", kind, got, label)
		}
	}
}
