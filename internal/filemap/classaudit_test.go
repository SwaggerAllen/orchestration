package filemap

import (
	"strings"
	"testing"
)

var componentGlobs = []string{"lib/app_web/components/**"}

// The hole §9 describes: a component arrives inside an artifact with
// nobody having said it would.
func TestClassAuditFlagsAnUnannouncedComponent(t *testing.T) {
	got := ClassAudit(componentGlobs,
		[]string{"lib/app_web/components/footer.ex"},
		"Add a farewell to the home screen. The hero gains a second line.")
	if len(got) != 1 {
		t.Fatalf("violations = %v, want one", got)
	}
	if !strings.Contains(got[0], "footer") || !strings.Contains(got[0], "components/footer.ex") {
		t.Errorf("violation %q names neither the component nor its path", got[0])
	}
}

// A design that names its new component in the hand-back — which the
// prompts require — passes without noticing the check exists.
func TestClassAuditPassesAnAnnouncedComponent(t *testing.T) {
	got := ClassAudit(componentGlobs,
		[]string{"lib/app_web/components/footer.ex"},
		"Decisions named rather than ported: a new Footer component module, because the farewell needs a home the hero cannot give it.")
	if len(got) != 0 {
		t.Errorf("flagged an announced component: %v", got)
	}
}

// Only *added* files are components arriving. Editing one every ticket
// is the normal case, and flagging it would make the audit noise.
func TestClassAuditIgnoresPathsOutsideTheComponentGlobs(t *testing.T) {
	added := []string{
		"screens/home.md",                   // design writes one most tickets
		"storybook/home.story.exs",          // and one of these
		"test/app_web/components/footer.ex", // a test, not a component
		"lib/app/billing/invoice.ex",        // a system module
	}
	if got := ClassAudit(componentGlobs, added, "nothing named here"); len(got) != 0 {
		t.Errorf("flagged non-components: %v", got)
	}
}

// A project that hasn't declared where its components live gets no
// findings — but the caller prints why, so this is not the same as a
// clean run. Asserted here so the empty-globs path stays deliberate.
func TestClassAuditWithoutComponentPathsFindsNothing(t *testing.T) {
	if got := ClassAudit(nil, []string{"lib/app_web/components/footer.ex"}, ""); got != nil {
		t.Errorf("violations = %v, want nil when the project declares no componentPaths", got)
	}
}

// Case is not the announcement. A hand-back saying "Footer" has named
// footer.ex, and a check that disagreed would be pedantry the author
// pays for.
func TestClassAuditMatchesNameCaseInsensitively(t *testing.T) {
	if got := ClassAudit(componentGlobs, []string{"lib/app_web/components/footer.ex"}, "a new Footer module"); len(got) != 0 {
		t.Errorf("case-different mention treated as unannounced: %v", got)
	}
}
