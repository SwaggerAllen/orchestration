package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/agent"
)

// The three halves must reach the model in the order they were written
// to be read: what your role is, then where you are, then the ticket.
// Orientation ahead of the role prompt would argue with it; orientation
// after the ticket would arrive as commentary on work already described.
func TestAssembledPromptOrdersRoleContextTicket(t *testing.T) {
	base := composeBase("ROLE-PROMPT", "REPO-CONTEXT")
	got := assemblePrompt(base, &agent.ClaimResult{
		TicketKey: "DUM-1", Title: "t", Mode: "fresh", Scope: "TICKET-SCOPE", Branch: "b",
	}, "/tmp/handback.md")

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
			Title: "Confirmed non-asks", Found: true,
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
	absent := nonAsksSection(&agent.NonAsks{Title: "Confirmed non-asks"}, "proposing")
	failed := nonAsksSection(&agent.NonAsks{Title: "Confirmed non-asks", Err: "linear: 403"}, "proposing")

	if absent == "" || failed == "" {
		t.Fatal("a section went missing; silence is exactly the ambiguity this removes")
	}
	if !strings.Contains(absent, "has no") {
		t.Errorf("absent section does not say the project has none:\n%s", absent)
	}
	if !strings.Contains(failed, "could not be read") || !strings.Contains(failed, "linear: 403") {
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
	if got := nonAsksSection(nil, "proposing"); got != "" {
		t.Errorf("nonAsksSection(nil) = %q, want empty", got)
	}
	if got := nonAsksSection(&agent.NonAsks{}, "proposing"); got != "" {
		t.Errorf("nonAsksSection(untitled) = %q, want empty", got)
	}
}
