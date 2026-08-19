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
	absent := nonAsksSection(&agent.NonAsks{Path: "non-asks.md"}, "proposing", false)
	failed := nonAsksSection(&agent.NonAsks{Path: "non-asks.md", Err: "permission denied"}, "proposing", false)

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
	if got := nonAsksSection(nil, "proposing", false); got != "" {
		t.Errorf("nonAsksSection(nil) = %q, want empty", got)
	}
	if got := nonAsksSection(&agent.NonAsks{}, "proposing", false); got != "" {
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
