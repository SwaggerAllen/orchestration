package agent

import (
	"strings"
	"testing"
)

func hunk(ours, theirs string) string {
	return "<<<<<<< HEAD\n" + ours + "\n=======\n" + theirs + "\n>>>>>>> origin/main\n"
}

func one(t *testing.T, path, body string) (park []Parked, attempt []string) {
	t.Helper()
	return Triage([]Conflict{{Path: path, Body: body}})
}

// The case ticket-scoped ids exist for. `internal/reasons` records
// ORC-246 and ORC-247 both minting generation#52 from one main; ids carry
// their minter so that cannot happen. Two sides naming *different* ids
// are therefore two additions that landed in the same place, and keeping
// both is the resolution — parking them would park the very case the
// scheme makes safe.
func TestTriageAttemptsTwoAdditionsWithTheirOwnIDs(t *testing.T) {
	park, attempt := one(t, "systems/engine.md", hunk(
		"- **#ORC-246-1 Retries** are the caller's.",
		"- **#ORC-247-1 Timeouts** are the engine's.",
	))
	if len(park) != 0 || len(attempt) != 1 {
		t.Fatalf("two additions parked: park=%+v attempt=%v", park, attempt)
	}

	// The same shape with one id on both sides must park, and it is here
	// rather than in its own test because together they are what proves
	// the two sides are read apart at all. Collapsing both sides into one
	// list passes the attempt case on its own.
	park, attempt = one(t, "systems/engine.md", hunk(
		"- **#ORC-246-1 Retries** are the caller's.",
		"- **#ORC-246-1 Retries** are the engine's.",
	))
	if len(park) != 1 || len(attempt) != 0 {
		t.Fatalf("one id on both sides was not parked: park=%+v attempt=%v", park, attempt)
	}
}

func TestTriageParksWhenBothSidesChangeTheSameRule(t *testing.T) {
	park, attempt := one(t, "systems/engine.md", hunk(
		"- **#17 Retries** are the caller's.",
		"- **#17 Retries** are the engine's.",
	))
	if len(park) != 1 || len(attempt) != 0 {
		t.Fatalf("park=%+v attempt=%v", park, attempt)
	}
	if !strings.Contains(park[0].Why, "#17") {
		t.Errorf("the park does not name the rule: %q", park[0].Why)
	}
}

// The same dispute arriving without the id in the hunk: both sides are
// rewriting one rule's prose, and the heading above says which.
func TestTriageParksProseInsideARuleSection(t *testing.T) {
	body := "## #17 Retries\n\nsettled prose\n\n" + hunk("the timeout is 30s", "the timeout is 60s")
	park, attempt := one(t, "systems/engine.md", body)
	if len(park) != 1 || len(attempt) != 0 {
		t.Fatalf("park=%+v attempt=%v", park, attempt)
	}
	if !strings.Contains(park[0].Why, "#17") {
		t.Errorf("the park does not name the enclosing rule: %q", park[0].Why)
	}
}

func TestTriageAttemptsWhereNoRuleIsInPlay(t *testing.T) {
	park, attempt := one(t, "lib/engine/retry.ex", hunk("@timeout 30", "@timeout 60"))
	if len(park) != 0 || len(attempt) != 1 {
		t.Fatalf("park=%+v attempt=%v", park, attempt)
	}
}

// A reasons sibling is not special-cased. Two tickets appending their own
// entries is an addition collision; both amending one entry's body is the
// enclosing-section case.
func TestTriageReadsAReasonsFileByItsEntries(t *testing.T) {
	t.Run("two entries appended", func(t *testing.T) {
		body := "## #17 Retries\n\nolder reasoning\n\n" + hunk(
			"## #ORC-246-1 Caller retries\n\nbecause the caller knows the budget.",
			"## #ORC-247-1 Engine timeouts\n\nbecause the engine owns the socket.",
		)
		park, attempt := one(t, "systems/engine.reasons.md", body)
		if len(park) != 0 || len(attempt) != 1 {
			t.Fatalf("two new entries parked: park=%+v attempt=%v", park, attempt)
		}
	})
	t.Run("one entry's body in dispute", func(t *testing.T) {
		body := "## #17 Retries\n\n" + hunk("because the caller knows the budget.", "because the engine owns the socket.")
		park, attempt := one(t, "systems/engine.reasons.md", body)
		if len(park) != 1 || len(attempt) != 0 {
			t.Fatalf("park=%+v attempt=%v", park, attempt)
		}
	})
}

// diff3 leaves the base between ||||||| and =======. A rule named only
// there is what both sides diverged *from*, not something either asserts.
//
// Shaped so the bug is visible: only `theirs` names the rule, so reading
// the base as a side would put it in `ours` too, intersect, and park a
// conflict where one side never mentioned the rule at all. An earlier
// version of this test had neither side naming it, which the bug also
// passes — a probe that broke the marker left the suite green.
func TestTriageIgnoresTheDiff3BaseSection(t *testing.T) {
	body := "<<<<<<< HEAD\nthe caller retries\n" +
		"||||||| base\n- **#17 Retries** are undecided.\n" +
		"=======\n- **#17 Retries** are the engine's.\n>>>>>>> origin/main\n"
	park, attempt := one(t, "lib/engine/retry.ex", body)
	if len(park) != 0 || len(attempt) != 1 {
		t.Fatalf("the base section was read as a side: park=%+v attempt=%v", park, attempt)
	}
}

func TestTriageSplitsAMixedMerge(t *testing.T) {
	park, attempt := Triage([]Conflict{
		{Path: "lib/b.ex", Body: hunk("x", "y")},
		{Path: "systems/engine.md", Body: hunk("- **#17 a**", "- **#17 b**")},
		{Path: "lib/a.ex", Body: hunk("x", "y")},
	})
	if len(park) != 1 || len(attempt) != 2 {
		t.Fatalf("park=%+v attempt=%v", park, attempt)
	}
	if attempt[0] != "lib/a.ex" || attempt[1] != "lib/b.ex" {
		t.Errorf("attempt is unordered: %v", attempt)
	}
}

func TestConflictReportNamesPathsAndRules(t *testing.T) {
	park, attempt := Triage([]Conflict{
		{Path: "systems/engine.md", Body: hunk("- **#17 a**", "- **#17 b**")},
		{Path: "lib/a.ex", Body: hunk("x", "y")},
	})
	r := ConflictReport(park, attempt)
	for _, want := range []string{"systems/engine.md", "#17", "lib/a.ex", "Ready for redesign"} {
		if !strings.Contains(r, want) {
			t.Errorf("the report does not carry %q:\n%s", want, r)
		}
	}
}
