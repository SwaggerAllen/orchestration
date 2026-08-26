package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/agent"
	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/retro"
)

// The three halves must reach the model in the order they were written
// to be read: what your role is, then where you are, then the ticket.
// Orientation ahead of the role prompt would argue with it; orientation
// after the ticket would arrive as commentary on work already described.
func TestAssembledPromptOrdersRoleContextTicket(t *testing.T) {
	base := composeBase("ROLE-PROMPT", "REPO-CONTEXT")
	got := assemblePrompt(base, &agent.ClaimResult{
		TicketKey: "DUM-1", Title: "t", Mode: "fresh", Scope: "TICKET-SCOPE", Branch: "b",
	}, "/tmp/handback.md", "/tmp/outcome.json")

	role, ctx, ticket := strings.Index(got, "ROLE-PROMPT"), strings.Index(got, "REPO-CONTEXT"), strings.Index(got, "TICKET-SCOPE")
	if role < 0 || ctx < 0 || ticket < 0 {
		t.Fatalf("a half went missing: role=%d context=%d ticket=%d", role, ctx, ticket)
	}
	if !(role < ctx && ctx < ticket) {
		t.Errorf("order = role %d, context %d, ticket %d; want role < context < ticket", role, ctx, ticket)
	}
}

// Without a repo-context path the prompt is unchanged, so a project
// pinned to an older pipeline ref keeps working rather than gaining a
// stray separator.
func TestComposeBaseWithoutContextIsUnchanged(t *testing.T) {
	if got := composeBase("ROLE", ""); got != "ROLE" {
		t.Errorf("composeBase(role, \"\") = %q, want the role prompt untouched", got)
	}
}

// The orientation only reaches a run if the action passes it, and that
// flag is exactly what a fifth agent would be written without. The file
// the flag names must exist for the same reason: claim fails hard on an
// unreadable path, mid-run.
func TestEveryAgentActionInjectsRepoContext(t *testing.T) {
	const flag = "--repo-context"
	root := filepath.Join("..", "..")
	actions, err := filepath.Glob(filepath.Join(root, ".github", "actions", "agent-*", "action.yml"))
	if err != nil || len(actions) == 0 {
		t.Fatalf("found no agent actions to check: %v", err)
	}
	for _, w := range actions {
		body, err := os.ReadFile(w)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), flag) {
			t.Errorf("%s runs an agent without %s — that run would not know what repo it is in", filepath.Dir(w), flag)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "prompts", "repo-context.md")); err != nil {
		t.Errorf("the workflows name prompts/repo-context.md but it is not there: %v", err)
	}
}

// The design agent's own hand-back reported this gap: "It lives on the
// Linear project (DESIGN §4) and this run has no tracker access, so I
// could not check the sketch's decisions against it." It was right — the
// agents have no tracker credentials by design (DESIGN §9), so the only
// way the document reaches a pass that proposes is in the prompt.
func TestDesignPromptCarriesTheNonAsksDocument(t *testing.T) {
	got := assembleDesignPrompt("ROLE", &agent.ClaimResult{
		TicketKey: "DUM-1", Title: "t", Mode: "design", Branch: "b",
		NonAsks: &agent.NonAsks{
			Path: "non-asks.md", Found: true,
			Body: "- No dark mode: two palettes, one designer.",
		},
	}, "/tmp/outcome.json")

	if !strings.Contains(got, "No dark mode") {
		t.Error("the prompt does not carry the document's body — the agent is back to guessing")
	}
	if strings.Index(got, "No dark mode") > strings.Index(got, "## Mechanics") {
		t.Error("the non-asks land after the mechanics; they are a constraint on the work, not a footnote")
	}
}

// The three outcomes must stay distinguishable in the rendered prompt.
// An absent document and an unreadable one produce the same empty
// section, and they license very different confidence: one says the
// author recorded no refusals, the other says nobody knows.
func TestNonAsksSectionSaysWhichOfTheThreeHappened(t *testing.T) {
	absent := nonAsksSection(&agent.NonAsks{Path: "non-asks.md"}, "proposing", false, nil)
	failed := nonAsksSection(&agent.NonAsks{Path: "non-asks.md", Err: "permission denied"}, "proposing", false, nil)

	if absent == "" || failed == "" {
		t.Fatal("a section went missing; silence is exactly the ambiguity this removes")
	}
	if !strings.Contains(absent, "has no") {
		t.Errorf("absent section does not say the project has none:\n%s", absent)
	}
	if !strings.Contains(failed, "could not be read") || !strings.Contains(failed, "permission denied") {
		t.Errorf("failed section does not say the read failed, or hides why:\n%s", failed)
	}
	if absent == failed {
		t.Error("absent and unreadable render identically — the distinction is the point")
	}
}

// A pass with no configured title renders nothing rather than an empty
// heading: a project that has opted out shouldn't get a section telling
// the model about a document nobody asked for.
func TestNonAsksSectionIsEmptyWhenUnconfigured(t *testing.T) {
	if got := nonAsksSection(nil, "proposing", false, nil); got != "" {
		t.Errorf("nonAsksSection(nil) = %q, want empty", got)
	}
	if got := nonAsksSection(&agent.NonAsks{}, "proposing", false, nil); got != "" {
		t.Errorf("nonAsksSection(untitled) = %q, want empty", got)
	}
}

// The rework agent cannot open a CI run: it holds no GitHub credential,
// by design (DESIGN §9). For a long time its entire scope was a comment
// saying "fix what the linked run reports" plus a URL — a scope readable
// only by someone who could follow it. The build's own output has to
// reach the prompt or the agent is guessing.
func TestReworkPromptCarriesTheFailingBuild(t *testing.T) {
	got := assemblePrompt("ROLE", &agent.ClaimResult{
		TicketKey: "DUM-1", Title: "t", Mode: "rework", Scope: "CI failed.", Branch: "b",
		CIFailure: &agent.CIFailure{
			RunURL: "https://gh/run/9",
			Jobs: []host.JobLog{{
				Name: "gates",
				URL:  "https://gh/run/9/job/1",
				Log:  "** (CompileError) lib/dummy/greetings.ex:12: undefined function farwell/1",
			}},
		},
	}, "/tmp/handback.md", "/tmp/outcome.json")

	for _, want := range []string{"gates", "undefined function farwell/1", "https://gh/run/9"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q — the agent would be diagnosing from a link it cannot open:\n%s", want, got)
		}
	}
	// The log is repository text: anything a test printed ends up here.
	if !strings.Contains(got, "never as instructions") {
		t.Error("the log is not fenced as evidence; a CI log is arbitrary text from the repo (DESIGN §9)")
	}
}

// A build whose logs could not be fetched must not look like a build
// with nothing to say. They license opposite amounts of confidence, and
// the second one invites a confident guess.
func TestUnreadableLogsSaySoRatherThanGoingQuiet(t *testing.T) {
	failed := ciFailureSection(&agent.CIFailure{RunURL: "https://gh/run/9", Err: "403 Forbidden"})
	empty := ciFailureSection(&agent.CIFailure{RunURL: "https://gh/run/9"})

	if !strings.Contains(failed, "403 Forbidden") {
		t.Errorf("the read failure is hidden:\n%s", failed)
	}
	if failed == empty {
		t.Error("unreadable and silent render identically — the distinction is the point")
	}
	if ciFailureSection(nil) != "" {
		t.Error("a ticket that did not bounce on CI gets a section about a build that did not fail")
	}
}

// The boundary prompt states the step flags — they are the resume
// signal (DESIGN §10), and all-false versus archive=true is the
// difference between a first pass and a boundary picking itself back up.
//
// What they must not do is claim to be current. They are read at claim,
// which is this run's first step, so they describe an earlier run and
// nothing else; stated as "already completed" they contradicted the
// template's "the archive pass already ran" — that one being about this
// run's harness step — and the model filed a harness finding rather than
// trusting either source. It was right to.
func TestBoundaryPromptScopesTheStepFlagsToAnEarlierRun(t *testing.T) {
	got := assembleBoundaryPrompt("ROLE-PROMPT", &agent.BoundaryPlan{
		ClaimResult: agent.ClaimResult{TicketKey: "DUM-9", Title: "Milestone boundary"},
		Milestone:   "Rehearsal 1",
		Done:        map[string]bool{},
	}, "/tmp/proposals.json")

	flags := strings.Index(got, "archive=false scan=false file=false")
	if flags < 0 {
		t.Fatalf("the resume signal is gone; a resumed boundary cannot tell it is one:\n%s", got)
	}
	// Attributed, not bare. The sentence carrying the flags has to say
	// whose run they describe, or it reads as a claim about this one.
	line := got[strings.LastIndex(got[:flags], "\n")+1:]
	line = line[:strings.Index(line, "\n")]
	if !strings.Contains(line, "earlier run") {
		t.Errorf("the flags are stated without saying they are an earlier run's: %q", line)
	}
	if !strings.Contains(got, retro.Dir) {
		t.Errorf("the prompt never names %s, which its own scan instructions call the duplicate detector:\n%s", retro.Dir, got)
	}
}

// And the same sentence reconciles itself with the file sitting beside
// it. `claim.json`'s Done map is the live resume record; these flags are
// frozen at claim, and `cmdBoundaryArchive` rewrites the former between
// this prompt being written and being read. So on every boundary run the
// two disagree, and both are right.
//
// A model with the run's temp directory in front of it has filed that
// contradiction as a harness finding twice — the carried
// `boundary-prompt-step-flags-always-false`, and Catapult's ORC-137,
// whose first diagnosis was that the two "should agree by construction"
// and whose proposed test would have passed. Making them agree is the
// wrong fix: it would set archive=true unconditionally and destroy the
// fresh-vs-resumed signal the test above covers. Saying so is the fix,
// and it belongs where the confused reader is reading.
func TestBoundaryPromptReconcilesItsFlagsWithTheClaimRecord(t *testing.T) {
	got := assembleBoundaryPrompt("ROLE-PROMPT", &agent.BoundaryPlan{
		ClaimResult: agent.ClaimResult{TicketKey: "DUM-9", Title: "Milestone boundary"},
		Milestone:   "Rehearsal 1",
		Done:        map[string]bool{},
	}, "/tmp/proposals.json")

	for _, want := range []string{
		"claim.json",  // names the other artifact
		"frozen",      // says which of the two is a snapshot
		"live",        // and which is not
		"not a fault", // and that the disagreement is expected
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the step-flags header does not mention %q, so a reader comparing it "+
				"with claim.json sees a bare contradiction:\n%s", want, got)
		}
	}
}

// The agent is judged against its labels and could not see them.
//
// CI fails a diff touching a path mapped to a screen or system doc whose
// label the ticket does not carry, and prompts/dev.md tells the agent to
// stay inside its labels — while claim.json had no Labels key at all, so
// the binding was to a set the run never received. Inferring them from
// the scope is the guess the mutex exists to prevent.
func TestDevPromptStatesTheLabelsTheRunIsJudgedAgainst(t *testing.T) {
	got := assemblePrompt("ROLE-PROMPT", &agent.ClaimResult{
		TicketKey: "DUM-1", Title: "t", Mode: "dev", Scope: "s", Branch: "b",
		Labels: []string{"frontend", "screen:home", "system:greetings"},
	}, "/tmp/handback.md", "/tmp/outcome.json")

	for _, want := range []string{"screen:home", "system:greetings", "frontend"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt never names %q, which CI will audit the diff against", want)
		}
	}
}

// No labels and "nobody told me" are different facts to an agent
// deciding whether a path is in bounds, and an absent section reads as
// the second — so the empty case is stated rather than skipped.
func TestDevPromptSaysSoWhenTheTicketCarriesNoLabels(t *testing.T) {
	got := assemblePrompt("ROLE-PROMPT", &agent.ClaimResult{
		TicketKey: "DUM-1", Title: "t", Mode: "dev", Scope: "s", Branch: "b",
	}, "/tmp/handback.md", "/tmp/outcome.json")

	if !strings.Contains(got, "Your labels") || !strings.Contains(got, "carries none") {
		t.Errorf("a ticket with no labels gets no section, which reads as the harness staying silent:\n%s", got)
	}
}

// The design pass is bound by the project's designOwnedPaths, and the
// only place it can read them is this prompt: pipeline.config.json is
// not something the agent is told to open, and the bound used to be a
// hardcoded list of file extensions in design.md instead (DESIGN §5).
func TestDesignPromptStatesTheOwnershipBoundary(t *testing.T) {
	got := assembleDesignPrompt("ROLE", &agent.ClaimResult{
		TicketKey: "DUM-1", Title: "t", Mode: "design", Branch: "b",
		DesignOwnedPaths: []string{"screens/*.md", "lib/app_web/components/**"},
	}, "/tmp/outcome.json")

	for _, want := range []string{"screens/*.md", "lib/app_web/components/**"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt does not name %q — the pass is bound by a list it cannot read", want)
		}
	}
	// Widening is the repair a blocked pass reaches for, and it is the
	// one that gets the push rejected and takes the run down.
	if !strings.Contains(got, "author-only") {
		t.Error("the prompt does not say the config is author-only")
	}
	if strings.Index(got, "screens/*.md") > strings.Index(got, "## Mechanics") {
		t.Error("the boundary lands after the mechanics; it is a constraint on the work, not a footnote")
	}
}

// Empty and unstated are different facts. No boundary passed means the
// harness did not say, which is not the same as the project owning
// nothing — and an absent section reads as the second.
func TestDesignBoundsSectionSaysWhenItWasNotTold(t *testing.T) {
	got := designBoundsSection(nil)
	if got == "" {
		t.Fatal("an empty boundary renders nothing; the pass reads that as no boundary at all")
	}
	if !strings.Contains(got, "harness finding") {
		t.Error("the gap is not routed anywhere it gets fixed")
	}
}

const scopedNonAsks = `# Confirmed non-asks

## No dark mode
scope: universal

Two palettes, one designer.

## No client-side validation on the cap form
scope: screen:cap

The server is the only authority.

## No pagination in the roster
scope: screen:roster

Thirty rows is the ceiling.
`

// A pass reads the refusals that bind it, not all of them. The document
// grows by rule — never delete an entry — so on a real project it
// reached 83468 bytes and every pass carried all of it.
func TestNonAsksSectionSelectsByTheTicketsScope(t *testing.T) {
	got := nonAsksSection(
		&agent.NonAsks{Path: "non-asks.md", Found: true, Body: scopedNonAsks},
		"proposing", true,
		&ticketScope{Labels: []string{"screen:roster"}},
	)
	if !strings.Contains(got, "Thirty rows") || !strings.Contains(got, "Two palettes") {
		t.Errorf("dropped the scoped or the universal entry: %s", got)
	}
	if strings.Contains(got, "only authority") {
		t.Errorf("carried a refusal about another screen: %s", got)
	}
}

// A filtered list that does not say it is filtered reads as the whole
// document, and "the non-asks do not mention it" becomes a conclusion
// the pass had no grounds for. The file is in the checkout, so the
// honest form is "here is your slice, the rest is one cat away".
func TestNonAsksSectionSaysWhatItLeftOut(t *testing.T) {
	got := nonAsksSection(
		&agent.NonAsks{Path: "docs/non-goals.md", Found: true, Body: scopedNonAsks},
		"proposing", true,
		&ticketScope{Labels: []string{"screen:roster"}},
	)
	if !strings.Contains(got, "2 of 3 entries") {
		t.Errorf("the section does not say it is a selection: %s", got)
	}
	if !strings.Contains(got, "docs/non-goals.md") {
		t.Errorf("the section does not say where the rest is: %s", got)
	}
}

// A first design pass carries no mutex labels — the design pass is what
// creates them — so selection has to fall back to the ticket's words or
// design, the pass this document is written for, sees only the
// universal set.
func TestNonAsksSectionSelectsOnTheTicketsWordsWhenItHasNoLabels(t *testing.T) {
	got := nonAsksSection(
		&agent.NonAsks{Path: "non-asks.md", Found: true, Body: scopedNonAsks},
		"proposing", true,
		&ticketScope{Text: "Cap screen: show cap_reached when the limit is hit"},
	)
	if !strings.Contains(got, "only authority") {
		t.Errorf("a ticket about the cap screen was not shown the cap refusal: %s", got)
	}
}

// The boundary files proposals across the project and has no single
// ticket's scope, so it reads the whole document.
func TestNonAsksSectionUnfilteredForTheBoundary(t *testing.T) {
	got := nonAsksSection(
		&agent.NonAsks{Path: "non-asks.md", Found: true, Body: scopedNonAsks},
		"filing proposals", false, nil,
	)
	for _, want := range []string{"Two palettes", "only authority", "Thirty rows"} {
		if !strings.Contains(got, want) {
			t.Errorf("the boundary lost %q: %s", want, got)
		}
	}
	if strings.Contains(got, "entries**, selected") {
		t.Error("the boundary was told its whole-document read was a selection")
	}
}

// Selecting nothing and having no file are different facts, the same
// way the three read outcomes are.
func TestNonAsksSectionDistinguishesAnEmptySelectionFromAnEmptyFile(t *testing.T) {
	// A document whose every entry is scoped elsewhere — with a
	// universal entry present there is no such thing as an empty
	// selection, which is the point of universal.
	scopedOnly := "## No pagination in the roster\nscope: screen:roster\n\nThirty rows is the ceiling.\n"
	none := nonAsksSection(
		&agent.NonAsks{Path: "non-asks.md", Found: true, Body: scopedOnly},
		"implementing", false,
		&ticketScope{Labels: []string{"system:unrelated"}},
	)
	if !strings.Contains(none, "That is a selection, not an empty file") {
		t.Errorf("an empty selection reads as a project with no refusals: %s", none)
	}
}

// The dev pass never saw this document at all. "No client-side
// validation on the cap form" binds whoever writes the validation, and
// that is dev.
func TestDevPromptCarriesTheNonAsksItIsBoundBy(t *testing.T) {
	got := assemblePrompt("ROLE", &agent.ClaimResult{
		TicketKey: "DUM-1", Title: "t", Mode: "dev", Branch: "b", Scope: "s",
		Labels:  []string{"screen:cap"},
		NonAsks: &agent.NonAsks{Path: "non-asks.md", Found: true, Body: scopedNonAsks},
	}, "/tmp/handback.md", "/tmp/outcome.json")

	if !strings.Contains(got, "only authority") {
		t.Errorf("the dev prompt does not carry the refusal binding its screen: %s", got)
	}
	if strings.Contains(got, "Thirty rows") {
		t.Errorf("the dev prompt carries an unrelated screen's refusal: %s", got)
	}
	// Read-only: design maintains the file, in the same commit as its
	// artifacts.
	if strings.Contains(got, "yours to maintain") {
		t.Error("the dev prompt tells dev to maintain a design-owned document")
	}
}

// Three empty states, not two. A project that has recorded nothing yet,
// a project whose refusals are all scoped elsewhere, and a project with
// no file license different confidence in a proposal, and only one of
// them is permission.
func TestNonAsksSectionDistinguishesAllThreeEmptyStates(t *testing.T) {
	recordedNone := nonAsksSection(
		&agent.NonAsks{Path: "non-asks.md", Found: true, Body: "# Confirmed non-asks\n"},
		"proposing", true, &ticketScope{Labels: []string{"screen:cap"}},
	)
	if !strings.Contains(recordedNone, "records no refusals yet") {
		t.Errorf("a fresh file reads as something else: %s", recordedNone)
	}
	if strings.Contains(recordedNone, "Confirmed non-asks\n---") {
		t.Errorf("the document title was rendered as a refusal: %s", recordedNone)
	}
	noFile := nonAsksSection(&agent.NonAsks{Path: "non-asks.md"}, "proposing", true, &ticketScope{})
	if !strings.Contains(noFile, "The repo has no") {
		t.Errorf("an absent file reads as something else: %s", noFile)
	}
}

// The retry has to be told the branch already carries work, and the two
// non-answers have to be distinguishable: "nothing is here" is a fact,
// "the harness did not tell me" is a bug (ORC-73).
func TestPriorWorkSectionSaysWhichOfTheThreeHappened(t *testing.T) {
	dir := t.TempDir()

	if got, err := priorWorkSection(""); err != nil || got != "" {
		t.Errorf("no --prior-work: got %q, %v — want the section omitted, as at claim time", got, err)
	}

	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := priorWorkSection(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "first pass") {
		t.Errorf("empty log rendered %q, want it to state that the branch carries nothing", got)
	}

	full := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(full, []byte("209fc9d design: home screen states\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = priorWorkSection(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"209fc9d", "resuming", "non-asks"} {
		if !strings.Contains(got, want) {
			t.Errorf("populated log rendered %q, want it to mention %q", got, want)
		}
	}

	// Loud, not absent: the action writes this file in the same job
	// immediately before reprompt reads it, so unreadable means the
	// harness is broken — and the failure it would otherwise cause is
	// the one this section exists to prevent, arriving silently.
	if _, err := priorWorkSection(filepath.Join(dir, "not-there.txt")); err == nil {
		t.Error("an unreadable --prior-work file returned no error")
	}
}

// The Go half of ORC-73 is inert unless the action writes the files and
// passes them, and neither half fails visibly on its own: a prompt with
// no branch section looks exactly like a first pass, which is the failure
// being fixed.
//
// Both agents, because both claim before their checkout. Dev was left out
// of the first cut and put back in the same milestone; a test that names
// only the action that happened to be filed about is how the second one
// gets forgotten again.
func TestBothWorkingAgentsRecordAndPassWhatTheBranchAlreadyCarries(t *testing.T) {
	for _, action := range []string{"agent-design", "agent-dev"} {
		body, err := os.ReadFile(filepath.Join("..", "..", ".github", "actions", action, "action.yml"))
		if err != nil {
			t.Fatal(err)
		}
		yml := string(body)
		for _, want := range []struct{ what, why string }{
			{"prior-work.txt", "the branch's log has to be written somewhere the prompt step can read"},
			{"--prior-work", "reprompt renders the section only when it is handed the file"},
			{"agent reprompt", "a prompt assembled at claim was assembled against main, not the branch"},
			{"pushed.txt", "the push step has to record the head it left behind"},
			{"--pushed-file", "abort puts the pushed head on the blocked marker only when it is handed the file"},
		} {
			if !strings.Contains(yml, want.what) {
				t.Errorf("%s does not mention %s — %s", action, want.what, want.why)
			}
		}
		// Order is the whole of the second half: written after the push
		// has landed, so a run that died before it pushed records nothing
		// rather than a head that never reached origin.
		push, rev := strings.Index(yml, "git push -u origin"), strings.Index(yml, "git rev-parse HEAD > ")
		if rev < 0 || push < 0 || rev < push {
			t.Errorf("%s writes pushed.txt at %d and pushes at %d — it must be written after the push lands", action, rev, push)
		}
		// And the rebuild has to run after the checkout that produces the
		// branch, or it rebuilds against the same tree the claim saw.
		checkout, rebuild := strings.Index(yml, "check out the ticket branch"), strings.Index(yml, "agent reprompt")
		if rebuild < checkout {
			t.Errorf("%s rebuilds the prompt at %d, before the checkout at %d", action, rebuild, checkout)
		}
	}
}

// The guard that used to admit design alone. Its stated reason — that
// every other kind "assembles its prompt against a tree that is already
// right" — was never measured and was false for dev, whose claim runs at
// the same point in its action, ahead of the checkout.
func TestRepromptAdmitsTheTwoKindsThatClaimBeforeTheirCheckout(t *testing.T) {
	dir := t.TempDir()
	tpl := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(tpl, []byte("role"), 0o644); err != nil {
		t.Fatal(err)
	}
	const refusal = "does not read the ticket branch"

	for _, kind := range []string{"design", "dev"} {
		err := cmdAgentReprompt([]string{"--kind", kind, "--out", dir, "--prompt-template", tpl})
		// It still fails — there is no config here — but it must fail
		// past the guard rather than at it.
		if err != nil && strings.Contains(err.Error(), refusal) {
			t.Errorf("%s was refused: %v", kind, err)
		}
	}
	for _, kind := range []string{"reconcile", "boundary"} {
		err := cmdAgentReprompt([]string{"--kind", kind, "--out", dir, "--prompt-template", tpl})
		if err == nil || !strings.Contains(err.Error(), refusal) {
			t.Errorf("%s: err = %v, want the refusal naming why this kind needs no rebuild", kind, err)
		}
	}
}

// claim.json and prompt.md are two artifacts of one claim, and they have
// to agree about what the branch says.
//
// The rebuild used to correct NonAsks in memory, render the prompt from
// it, print what changed, and leave claim.json holding the base commit's
// copy. ORC-73 measured exactly that and said the prompt "was correct in
// the place that mattered" while the claim data "reflects the base
// rather than the branch's actual state".
func TestRepromptRewritesTheClaimRecordAndNotJustThePrompt(t *testing.T) {
	const onTheBranch = "# Confirmed non-asks\n\n## No dark mode\nscope: universal\n\nTwo palettes, one designer.\n"
	cfgPath := nonAsksProject(t, onTheBranch)
	out := t.TempDir()

	tpl := filepath.Join(out, "template.md")
	if err := os.WriteFile(tpl, []byte("role"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The claim, as it was written before the branch checkout: the base
	// commit's copy of the file, which is the whole defect.
	claimed := agent.ClaimResult{
		TicketID: "iss_1", TicketKey: "PIPE-1", Title: "A ticket",
		Mode: "design", Scope: "do the thing",
		NonAsks: &agent.NonAsks{
			Path: config.DefaultNonAsksPath, Found: true,
			Body: "# Confirmed non-asks\n\n(the base commit's copy, one entry short)\n",
		},
	}
	raw, err := json.MarshalIndent(&claimed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "claim.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cmdAgentReprompt([]string{
		"--kind", "design", "--config", cfgPath, "--out", out, "--prompt-template", tpl,
	}); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(filepath.Join(out, "claim.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rebuilt agent.ClaimResult
	if err := json.Unmarshal(after, &rebuilt); err != nil {
		t.Fatal(err)
	}
	if rebuilt.NonAsks == nil || rebuilt.NonAsks.Body != onTheBranch {
		got := "<nil>"
		if rebuilt.NonAsks != nil {
			got = rebuilt.NonAsks.Body
		}
		t.Errorf("claim.json still carries the base commit's non-asks:\n got %q\nwant %q", got, onTheBranch)
	}
	// The rest of the claim has to survive the round trip — this rewrites
	// the record, it does not replace it.
	if rebuilt.TicketKey != claimed.TicketKey || rebuilt.Scope != claimed.Scope || rebuilt.Mode != claimed.Mode {
		t.Errorf("the rewrite lost part of the claim: %+v", rebuilt)
	}
	// And the prompt still carries it, which is the half that was already
	// right and must stay right.
	prompt, err := os.ReadFile(filepath.Join(out, "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "No dark mode") {
		t.Error("prompt.md lost the branch's non-asks")
	}
}

// Design is the pass that can fold an accepted delta into the sketch, so
// it is the one that has to be told the thread carries them.
//
// The asymmetry this covers: assembleReconcilePrompt already says the
// comments carry the accepted deltas, and reconcile only judges against
// them. Design's header was bare, and a pass read the thread as history
// — declining to widen on scope its own comments had already accepted.
func TestDesignPromptSaysTheCommentsCarryDeltasThatMayWiden(t *testing.T) {
	got := assembleDesignPrompt("ROLE", &agent.ClaimResult{
		TicketKey: "PIPE-1", Title: "A ticket", Mode: "design",
		Description: "the original argument",
		Comments:    []string{"the author accepted more scope here"},
	}, "")

	if !strings.Contains(got, "the author accepted more scope here") {
		t.Fatal("the thread never reached the prompt at all")
	}
	head := got[strings.Index(got, "## Comments"):]
	head = head[:strings.Index(head, "\n")]
	for _, want := range []string{"deltas", "widen"} {
		if !strings.Contains(head, want) {
			t.Errorf("the comments header is missing %q — without it a pass reads the thread as history:\n  %s", want, head)
		}
	}
}

// And the role prompt carries the rule, including the half that stops it
// becoming licence to widen on the pass's own reading.
func TestDesignRolePromptBoundsWhoMayWidenTheTicket(t *testing.T) {
	body := repoFile(t, "prompts/design.md")
	for _, want := range []string{
		"comments carry deltas", // the rule
		"immutable",             // why the thread is the only channel
		"push-back",             // scope nobody agreed stays one
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("prompts/design.md does not mention %q", want)
		}
	}
}
