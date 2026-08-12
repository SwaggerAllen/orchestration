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

// The orientation only reaches a run if the workflow passes it, and
// that flag is exactly what a fifth agent workflow would be written
// without. The file the flag names must exist for the same reason:
// claim fails hard on an unreadable path, mid-run.
func TestEveryAgentWorkflowInjectsRepoContext(t *testing.T) {
	const flag = "--repo-context"
	root := filepath.Join("..", "..")
	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "agent-*.yml"))
	if err != nil || len(workflows) == 0 {
		t.Fatalf("found no agent workflows to check: %v", err)
	}
	for _, w := range workflows {
		body, err := os.ReadFile(w)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), flag) {
			t.Errorf("%s runs an agent without %s — that run would not know what repo it is in", filepath.Base(w), flag)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "prompts", "repo-context.md")); err != nil {
		t.Errorf("the workflows name prompts/repo-context.md but it is not there: %v", err)
	}
}
