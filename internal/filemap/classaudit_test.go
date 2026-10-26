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

// The React layout, measured on Oneiroi's `client/src/components/**`
// and its design system: the component is the first word of the base
// name, `index` names its directory, and the files beside a component
// are not components. Before componentName every file here but the
// first two was reported, each as a name no ticket could reasonably
// carry ("button.stories", "index", "button.module").
func TestClassAuditReadsReactComponentNames(t *testing.T) {
	globs := []string{"client/src/components/**"}
	announced := "Decisions named rather than ported: a new Button component, so the approval bar has a primary action."
	added := []string{
		"client/src/components/Button.tsx",
		"client/src/components/Button/index.tsx",
		"client/src/components/Button/Button.stories.tsx",
		"client/src/components/Button/Button.test.tsx",
		"client/src/components/Button/Button.module.css",
		"client/src/components/Button/button.css",
	}
	if got := ClassAudit(globs, added, announced); len(got) != 0 {
		t.Errorf("announced React component flagged: %v", got)
	}
	unannounced := ClassAudit(globs, added, "nothing named here")
	if len(unannounced) != 2 {
		t.Fatalf("violations = %v, want the two component files and none of their companions", unannounced)
	}
	for _, v := range unannounced {
		if !strings.Contains(v, "\"Button\"") {
			t.Errorf("violation %q does not name the component Button", v)
		}
	}
}

// The Phoenix layout keeps working under the same rule: a test or a
// story beside a component is a companion in either naming convention.
func TestClassAuditTreatsElixirCompanionsAsNotComponents(t *testing.T) {
	added := []string{
		"lib/app_web/components/footer_test.exs",
		"lib/app_web/components/footer.story.exs",
	}
	if got := ClassAudit(componentGlobs, added, "nothing named here"); len(got) != 0 {
		t.Errorf("companions flagged as components: %v", got)
	}
}

func TestComponentName(t *testing.T) {
	cases := map[string]string{
		"lib/app_web/components/footer.ex":                "footer",
		"client/src/components/Footer.tsx":                "Footer",
		"client/src/components/Footer/index.tsx":          "Footer",
		"client/src/components/Footer/Footer.stories.tsx": "",
		"client/src/components/Footer/Footer.test.tsx":    "",
		"client/src/components/Footer/Footer.module.css":  "",
		"lib/app_web/components/footer_test.exs":          "",
	}
	for path, want := range cases {
		if got := componentName(path); got != want {
			t.Errorf("componentName(%q) = %q, want %q", path, got, want)
		}
	}
}
