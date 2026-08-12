package plane

import (
	"context"
	"strings"
	"testing"
)

func TestNonAsksReadsTheProjectDocument(t *testing.T) {
	tr, cfg, p := world(t)
	tr.AddDocument(cfg.Tracker.ProjectID, cfg.NonAsksDocument, "- No dark mode: two palettes, one designer.")
	tr.AddDocument(cfg.Tracker.ProjectID, "Roadmap", "not this one")

	body, found, err := p.NonAsks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("found = false; the document is right there")
	}
	if !strings.Contains(body, "No dark mode") {
		t.Errorf("body = %q, want the non-asks document's content", body)
	}
}

// A project with no such document is an ordinary answer, not an error:
// most projects start without one, and failing the claim over it would
// make the design agent unrunnable on a fresh project.
func TestNonAsksAbsentIsNotAnError(t *testing.T) {
	_, _, p := world(t)
	body, found, err := p.NonAsks(context.Background())
	if err != nil || found || body != "" {
		t.Errorf("NonAsks() = %q, %t, %v; want empty, false, nil", body, found, err)
	}
}

// The title is typed by a human into a tracker UI. "Confirmed Non-Asks"
// is the same document as "Confirmed non-asks" to everyone except a
// string comparison, and getting this wrong looks exactly like the bug
// this whole feature exists to fix: an agent told nothing was recorded.
func TestNonAsksMatchesTitleCaseInsensitively(t *testing.T) {
	tr, cfg, p := world(t)
	tr.AddDocument(cfg.Tracker.ProjectID, "  Confirmed Non-Asks  ", "body")

	cfg.NonAsksDocument = "confirmed non-asks"
	_, found, err := p.NonAsks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("found = false; the titles differ only in case and surrounding space")
	}
}

// Two documents by one name must not resolve by picking one — the agent
// would be told it had the author's constraints while holding half of
// them, which is worse than being told the read failed.
func TestNonAsksRefusesDuplicateTitles(t *testing.T) {
	tr, cfg, p := world(t)
	tr.AddDocument(cfg.Tracker.ProjectID, "Confirmed non-asks", "first")
	tr.AddDocument(cfg.Tracker.ProjectID, "Confirmed Non-Asks", "second")

	if _, found, err := p.NonAsks(context.Background()); err == nil {
		t.Errorf("NonAsks() found=%t, err=nil; want a refusal naming both documents", found)
	}
}

// Documents are per-project, and the design agent's scope is one project
// (DESIGN §2). Another project's non-asks are another author's decisions.
func TestNonAsksIsScopedToTheProject(t *testing.T) {
	tr, cfg, p := world(t)
	tr.AddDocument("project_elsewhere", cfg.NonAsksDocument, "someone else's refusals")

	if _, found, err := p.NonAsks(context.Background()); found || err != nil {
		t.Errorf("NonAsks() found=%t, err=%v; want false, nil — that document belongs to another project", found, err)
	}
}
