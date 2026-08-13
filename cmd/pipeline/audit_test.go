package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
)

// project writes a minimal but real project tree — config, one system
// doc, one screen doc — and returns its root.
func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	raw, err := json.Marshal(config.Sample())
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pipeline.config.json", string(raw))
	write("systems/billing.md", "---\npaths:\n  - lib/app/billing/**\n---\n\n# billing\n\n## Standing decisions\n\n- Prose.\n")
	write("screens/home.md", "---\nfiles:\n  - lib/sample_web/components/home.ex\n---\n\n# home\n\n## Standing decisions\n\n- Prose.\n")
	return root
}

func listFile(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "list")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// CI runs the audit as `go -C .pipeline run ./cmd/pipeline audit`,
// which puts the process's working directory inside the *pipeline*
// checkout — a tree with no systems/ or screens/. With --root
// defaulting to ".", the audit loaded zero maps, and zero maps cannot
// be violated: it printed "audit clean ... against 0 system and 0
// screen maps" and exited 0 on a diff that should have failed. The
// check passed because it was looking somewhere the answer could not
// be, which is the worst way for a check to be wrong — it is
// indistinguishable from working.
func TestAuditFindsTheProjectsDocsFromAnyWorkingDirectory(t *testing.T) {
	root := project(t)
	t.Chdir(t.TempDir()) // stand anywhere else, as the real run does

	err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--changed-files", listFile(t, "lib/app/billing/invoice.ex"),
		"--labels", "screen:home", // the wrong label for a billing path
	})
	if err == nil {
		t.Fatal("audit passed a mapped path carrying the wrong label — it found no maps to check against")
	}
	if !strings.Contains(err.Error(), "violations") {
		t.Errorf("error = %v, want the violation report", err)
	}
}

// The same diff with the label it needs must pass, or the test above
// would be satisfied by an audit that simply always fails.
func TestAuditPassesWhenTheLabelCoversTheTouch(t *testing.T) {
	root := project(t)
	t.Chdir(t.TempDir())

	if err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--changed-files", listFile(t, "lib/app/billing/invoice.ex"),
		"--labels", "system:billing",
	}); err != nil {
		t.Errorf("audit failed a covered touch: %v", err)
	}
}

// The doc lint rides on the same root, so it fails the same way and
// must be covered the same way.
func TestAuditRunsTheDocLintAgainstTheProjectsDocs(t *testing.T) {
	root := project(t)
	if err := os.WriteFile(filepath.Join(root, "screens", "home.md"),
		[]byte("---\nfiles:\n  - x.ex\n---\n\n# home\n\n## States\n\n- empty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--changed-files", listFile(t, "README.md"),
		"--labels", "system:billing",
	})
	if err == nil {
		t.Fatal("audit passed a screen doc carrying a state section")
	}
}

// An explicit --root still wins: the agents run the audit by hand from
// inside a project checkout, and that has to keep working.
func TestAuditHonoursAnExplicitRoot(t *testing.T) {
	root := project(t)
	t.Chdir(t.TempDir())

	if err := cmdAudit([]string{
		"--config", filepath.Join(root, "pipeline.config.json"),
		"--root", root,
		"--changed-files", listFile(t, "lib/app/billing/invoice.ex"),
		"--labels", "system:billing",
	}); err != nil {
		t.Errorf("explicit --root failed: %v", err)
	}
}
