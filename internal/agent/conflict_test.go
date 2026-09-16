package agent

import (
	"strings"
	"testing"
)

func hunk(ours, theirs string) string {
	return "before\n<<<<<<< HEAD\n" + ours + "\n=======\n" + theirs + "\n>>>>>>> origin/main\nafter\n"
}

func TestTriageParksAReasonsSibling(t *testing.T) {
	park, attempt := Triage([]Conflict{{Path: "systems/engine.reasons.md", Body: hunk("a", "b")}})
	if len(park) != 1 || len(attempt) != 0 {
		t.Fatalf("park=%v attempt=%v", park, attempt)
	}
	if !strings.Contains(park[0].Why, "rationale") {
		t.Errorf("why = %q", park[0].Why)
	}
}

func TestTriageParksAHunkNamingARule(t *testing.T) {
	for _, side := range []struct{ name, ours, theirs string }{
		{"ours names it", "- **#17 Retries** are the caller's.", "- Retries are the engine's."},
		{"theirs names it", "- Retries are the caller's.", "- **#17 Retries** are the engine's."},
		{"a citation", "this follows `engine#17`", "this does not"},
	} {
		t.Run(side.name, func(t *testing.T) {
			park, attempt := Triage([]Conflict{{Path: "systems/engine.md", Body: hunk(side.ours, side.theirs)}})
			if len(park) != 1 || len(attempt) != 0 {
				t.Fatalf("park=%v attempt=%v", park, attempt)
			}
			if !strings.Contains(park[0].Why, "#17") {
				t.Errorf("the park does not name the rule: %q", park[0].Why)
			}
		})
	}
}

// Regions, not files. A systems doc carries rule ids throughout, so
// parking on the file would park every conflict in every such doc — the
// label mutex's blast radius arriving by another route.
func TestTriageReadsTheHunkNotTheFile(t *testing.T) {
	body := "## #17 Retries\n\nsettled prose about the rule\n\n" +
		hunk("the timeout is 30s", "the timeout is 60s")
	park, attempt := Triage([]Conflict{{Path: "systems/engine.md", Body: body}})
	if len(park) != 0 || len(attempt) != 1 {
		t.Fatalf("a rule id outside the hunk parked the conflict: park=%v attempt=%v", park, attempt)
	}
}

func TestTriageLetsAnOrdinaryConflictThrough(t *testing.T) {
	park, attempt := Triage([]Conflict{{Path: "lib/engine/retry.ex", Body: hunk("@timeout 30", "@timeout 60")}})
	if len(park) != 0 || len(attempt) != 1 || attempt[0] != "lib/engine/retry.ex" {
		t.Fatalf("park=%v attempt=%v", park, attempt)
	}
}

func TestTriageSplitsAMixedMerge(t *testing.T) {
	park, attempt := Triage([]Conflict{
		{Path: "lib/b.ex", Body: hunk("x", "y")},
		{Path: "systems/engine.reasons.md", Body: hunk("x", "y")},
		{Path: "lib/a.ex", Body: hunk("x", "y")},
	})
	if len(park) != 1 || len(attempt) != 2 {
		t.Fatalf("park=%v attempt=%v", park, attempt)
	}
	if attempt[0] != "lib/a.ex" || attempt[1] != "lib/b.ex" {
		t.Errorf("attempt is unordered: %v", attempt)
	}
}

// The report is read by somebody who cannot see the tree and is deciding
// whether to re-decide the design.
func TestConflictReportNamesPathsAndRules(t *testing.T) {
	park, attempt := Triage([]Conflict{
		{Path: "systems/engine.md", Body: hunk("- **#17 Retries** are the caller's.", "- Retries are the engine's.")},
		{Path: "lib/a.ex", Body: hunk("x", "y")},
	})
	r := ConflictReport(park, attempt)
	for _, want := range []string{"systems/engine.md", "#17", "lib/a.ex", "Ready for redesign"} {
		if !strings.Contains(r, want) {
			t.Errorf("the report does not carry %q:\n%s", want, r)
		}
	}
}
