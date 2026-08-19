package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/agent"
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
