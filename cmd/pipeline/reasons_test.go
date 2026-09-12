package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/reasons"
)

// reasonsProject is portedProject(t) as a config path plus its root, the
// shape the command takes.
func reasonsProject(t *testing.T) (cfg, root string) {
	t.Helper()
	root = portedProject(t)
	return filepath.Join(root, "pipeline.config.json"), root
}

func runReasons(t *testing.T, cfg string, args ...string) (out, errOut string, err error) {
	t.Helper()
	t.Chdir(t.TempDir())
	errOut = captureStderr(t, func() {
		out = captureStdout(t, func() {
			err = cmdReasons(append([]string{"--config", cfg}, args...))
		})
	})
	return out, errOut, err
}

func TestReasonsCommandPrintsTheRuleAndItsEntry(t *testing.T) {
	cfg, _ := reasonsProject(t)
	out, _, err := runReasons(t, cfg, "billing#17")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"systems/billing.md#17\n- **#17 The cap is enforced server-side.** Prose.\n",
		"systems/billing.reasons.md:\n## #17\nsince: ORC-22\n\nA second copy of the rules drifts.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestReasonsCommandSaysWhenNoReasonsFileExists(t *testing.T) {
	cfg, root := reasonsProject(t)
	if err := os.Remove(filepath.Join(root, "systems", "billing.reasons.md")); err != nil {
		t.Fatal(err)
	}
	out, _, err := runReasons(t, cfg, "billing#17")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "- **#17 The cap is enforced server-side.**") || !strings.Contains(out, "No systems/billing.reasons.md exists: no reason is recorded for any rule in this doc. Checked, not skipped.") {
		t.Errorf("out = %s", out)
	}
}

func TestReasonsCommandSaysWhenTheEntryIsMissing(t *testing.T) {
	cfg, _ := reasonsProject(t)
	out, _, err := runReasons(t, cfg, "billing#1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## #1 Standing decisions\n") || !strings.Contains(out, "systems/billing.reasons.md records no entry for #1: the rule stands without a recorded reason.") {
		t.Errorf("out = %s", out)
	}
}

func TestReasonsCommandSaysWhenTheIdIsUnknownAndNamesTheHighest(t *testing.T) {
	cfg, _ := reasonsProject(t)
	out, _, err := runReasons(t, cfg, "billing#99")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "systems/billing.md#99: no rule carries this id and no entry records it. The doc's highest bare id is #17.") {
		t.Errorf("out = %s", out)
	}
	// A miss under a ticket names what that ticket has minted here, which
	// is the number the pass is about to need.
	out, _, err = runReasons(t, cfg, "billing#ORC-247-3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "The doc carries no ORC-247- ids yet; the first one this ticket mints here is #ORC-247-1.") {
		t.Errorf("out = %s", out)
	}
}

func TestReasonsCommandSaysWhenTheDocIsUnknown(t *testing.T) {
	cfg, _ := reasonsProject(t)
	out, _, err := runReasons(t, cfg, "home#1", "nothing#1")
	if err != nil {
		t.Fatal(err)
	}
	// home exists and is unported; nothing does not exist. Both are the
	// same answer: nothing there can be cited.
	if strings.Count(out, "Checked, not skipped.") != 2 || !strings.Contains(out, "home#1: no ported systems/home.md or screens/home.md") {
		t.Errorf("out = %s", out)
	}
}

func TestReasonsCommandRefusesAnAmbiguousBareName(t *testing.T) {
	cfg, root := reasonsProject(t)
	if err := os.WriteFile(filepath.Join(root, "screens", "billing.md"), []byte("---\nfiles:\n  - lib/sample_web/components/billing.ex\n---\n\n# billing\n\n## #1 One screen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runReasons(t, cfg, "billing#17")
	if err == nil || !strings.Contains(err.Error(), "is ambiguous: billing is both systems/billing.md and screens/billing.md") {
		t.Errorf("err = %v", err)
	}
	out, _, err := runReasons(t, cfg, "system:billing#17", "screen:billing#1")
	if err != nil || !strings.Contains(out, "systems/billing.md#17") || !strings.Contains(out, "screens/billing.md#1\n## #1 One screen") {
		t.Errorf("err = %v, out = %s", err, out)
	}
}

func TestReasonsCommandPrintsARetiredEntry(t *testing.T) {
	cfg, _ := reasonsProject(t)
	out, _, err := runReasons(t, cfg, "billing#9")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "systems/billing.md#9 is retired (retired: ORC-90 — the guard moved into the compiler). The rule line is gone from systems/billing.md; the entry is kept as the record of why.") || !strings.Contains(out, "## #9\nretired: ORC-90") {
		t.Errorf("out = %s", out)
	}
}

func TestReasonsCommandTakesSeveralIds(t *testing.T) {
	cfg, _ := reasonsProject(t)
	out, _, err := runReasons(t, cfg, "billing#17", "billing#9", "billing#99")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"systems/billing.md#17\n", "systems/billing.md#9 is retired", "systems/billing.md#99: no rule"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Said every time: "no reason is recorded" is not "there is no reason".
func TestReasonsCommandAlwaysStatesTheCaveat(t *testing.T) {
	cfg, root := reasonsProject(t)
	if err := os.Remove(filepath.Join(root, "systems", "billing.reasons.md")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"billing#17"}, {"billing#99"}, {"nothing#1"}} {
		_, errOut, err := runReasons(t, cfg, args...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(errOut, "A reason is a record, not a lock.") {
			t.Errorf("%v: stderr = %q", args, errOut)
		}
	}
}

func TestReasonsCommandRefusesAnEmptyOrMalformedQuery(t *testing.T) {
	cfg, _ := reasonsProject(t)
	if _, _, err := runReasons(t, cfg); err == nil || !strings.Contains(err.Error(), "name one or more rules") {
		t.Errorf("no args: err = %v", err)
	}
	if _, _, err := runReasons(t, cfg, "#17"); err == nil || !strings.Contains(err.Error(), "is not a rule citation") {
		t.Errorf("bare #17: err = %v", err)
	}
}

// The command and the audit resolve through one function, so what the
// audit accepts as a citation is what the command answers for.
func TestReasonsCommandAgreesWithTheAudit(t *testing.T) {
	cfg, root := reasonsProject(t)
	docs, err := reasons.LoadDir(root, "systems")
	if err != nil {
		t.Fatal(err)
	}
	for id, wantResolves := range map[string]bool{"17": true, "9": true, "1": true, "99": false} {
		ix, ok, _ := reasons.Resolve("", "billing", docs)
		if !ok {
			t.Fatal("billing did not resolve")
		}
		out, _, err := runReasons(t, cfg, "billing#"+id)
		if err != nil {
			t.Fatal(err)
		}
		if got := !strings.Contains(out, "no rule carries this id"); got != wantResolves || ix.Resolves(id) != wantResolves {
			t.Errorf("#%s: command resolves=%v, audit resolves=%v, want %v", id, got, ix.Resolves(id), wantResolves)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
